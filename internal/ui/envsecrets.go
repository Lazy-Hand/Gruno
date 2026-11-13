package ui

// Environment secrets: vars:secret names live in the environment file,
// their values in the encrypted secrets store (internal/secrets). The
// app caches each environment's values once read, so the credential
// store is asked at most once per collection.

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"

	"gruno/bru"
	"gruno/internal/script"
	"gruno/internal/secrets"
	"gruno/internal/send"
)

// secretsKeyName is the credential-store item holding the AES key.
const secretsKeyName = "env-secrets-key"

// keychainKey reads the encryption key from the system credential
// store, creating it on first use.
func keychainKey() ([]byte, error) {
	k, err := mygo.Secrets.Get(secretsKeyName)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, err
	}
	k, err = secrets.NewKey()
	if err != nil {
		return nil, err
	}
	if err := mygo.Secrets.Set(secretsKeyName, k); err != nil {
		return nil, err
	}
	return k, nil
}

// defaultSecretsStore is the app's store in PathUserData.
func defaultSecretsStore() *secrets.Store {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return nil
	}
	return secrets.New(filepath.Join(dir, "env-secrets.json"), keychainKey)
}

// secretName splits a vars:secret entry: a leading ~ disables it, as
// in dictionary blocks.
func secretName(s string) (name string, enabled bool) {
	if strings.HasPrefix(s, "~") {
		return s[1:], false
	}
	return s, true
}

func isSecret(env *bru.Environment, key string) bool {
	for _, s := range env.Secrets {
		if n, _ := secretName(s); n == key {
			return true
		}
	}
	return false
}

// envSecretValues returns env's secret values, reading the store on
// first use. A store failure is kept in secretErr for the UI and gives
// no values.
func (a *app) envSecretValues(env *bru.Environment) map[string]string {
	if env == nil || len(env.Secrets) == 0 || a.secrets == nil || a.coll == nil {
		return map[string]string{}
	}
	if a.secretCache == nil {
		a.secretCache = map[string]map[string]string{}
	}
	if v, ok := a.secretCache[env.Name]; ok {
		return v
	}
	v, err := a.secrets.Get(a.coll.Root, env.Name)
	if err != nil {
		// Not cached: the next call retries, so one keychain hiccup does
		// not silently drop every secret for the session.
		a.secretErr = err.Error()
		return map[string]string{}
	}
	a.secretErr = ""
	a.secretCache[env.Name] = v
	return v
}

// setEnvSecretValues stores env's secret values, dropping any whose
// name is no longer a secret.
func (a *app) setEnvSecretValues(env *bru.Environment, values map[string]string) error {
	kept := map[string]string{}
	for k, v := range values {
		if isSecret(env, k) && v != "" {
			kept[k] = v
		}
	}
	if a.secretCache == nil {
		a.secretCache = map[string]map[string]string{}
	}
	a.secretCache[env.Name] = kept
	if a.secrets == nil || a.coll == nil {
		return nil
	}
	return a.secrets.Set(a.coll.Root, env.Name, kept)
}

// secretPairs are env's enabled secrets with their values, for sending.
func (a *app) secretPairs(env *bru.Environment) []send.Pair {
	if env == nil || len(env.Secrets) == 0 {
		return nil
	}
	vals := a.envSecretValues(env)
	out := make([]send.Pair, 0, len(env.Secrets))
	for _, s := range env.Secrets {
		name, enabled := secretName(s)
		if v, ok := vals[name]; ok {
			out = append(out, send.Pair{Key: name, Value: v, Enabled: enabled})
		}
	}
	return out
}

// applySecretEnvSets takes the bru.setEnvVar calls that name a secret
// out of v.EnvSets and applies them to the secret values, writing the
// store when any asked for persist. The rest stay for applyEnvSets.
func (a *app) applySecretEnvSets(v *script.Vars) error {
	if a.coll == nil || len(v.EnvSets) == 0 {
		return nil
	}
	var env *bru.Environment
	for _, e := range a.coll.Environments {
		if e.Name == v.EnvName {
			env = e
		}
	}
	if env == nil || len(env.Secrets) == 0 {
		return nil
	}
	vals := map[string]string{}
	for k, val := range a.envSecretValues(env) {
		vals[k] = val
	}
	rest := v.EnvSets[:0:0]
	changed, persist := false, false
	for _, s := range v.EnvSets {
		if !isSecret(env, s.Key) {
			rest = append(rest, s)
			continue
		}
		vals[s.Key] = s.Value
		changed = true
		persist = persist || s.Persist
	}
	v.EnvSets = rest
	if !changed {
		return nil
	}
	if persist {
		return a.setEnvSecretValues(env, vals)
	}
	a.secretCache[env.Name] = vals
	return nil
}

// envRow is one row of the environment editor; Secret rows save to
// vars:secret and the secrets store.
type envRow struct {
	Key, Value      string
	Enabled, Secret bool
}

// beginEnvEdit opens the environment editor on a draft of env.
func (a *app) beginEnvEdit(env *bru.Environment) {
	a.envEditor = true
	a.envTarget = env
	a.envRows = a.envRows[:0]
	for _, e := range env.Vars {
		a.envRows = append(a.envRows, envRow{Key: e.Key, Value: e.Value, Enabled: e.Enabled})
	}
	vals := a.envSecretValues(env)
	for _, s := range env.Secrets {
		name, enabled := secretName(s)
		a.envRows = append(a.envRows, envRow{Key: name, Value: vals[name], Enabled: enabled, Secret: true})
	}
	a.revealSecret = map[int]bool{}
}

// saveEnvEdit writes the editor's rows to the environment file and the
// secrets store.
func (a *app) saveEnvEdit() error {
	env := a.envTarget
	env.Vars = nil
	env.Secrets = nil
	vals := map[string]string{}
	for _, r := range a.envRows {
		if strings.TrimSpace(r.Key) == "" {
			continue
		}
		if r.Secret {
			name := r.Key
			if !r.Enabled {
				name = "~" + name
			}
			env.Secrets = append(env.Secrets, name)
			vals[r.Key] = r.Value
			continue
		}
		env.Vars = append(env.Vars, bru.Entry{Key: r.Key, Value: r.Value, Enabled: r.Enabled})
	}
	if err := a.coll.SaveEnvironment(env); err != nil {
		return err
	}
	return a.setEnvSecretValues(env, vals)
}
