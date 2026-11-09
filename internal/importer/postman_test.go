package importer

import (
	"strings"
	"testing"
)

const samplePostman = `{
  "info": {"name": "Shop API", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
  "variable": [{"key": "baseUrl", "value": "https://api.test"}, {"key": "retries", "value": 3}],
  "auth": {"type": "bearer", "bearer": [{"key": "token", "value": "{{token}}"}]},
  "item": [
    {
      "name": "Users",
      "auth": {"type": "basic", "basic": [{"key": "username", "value": "ada"}, {"key": "password", "value": "pw"}]},
      "item": [
        {
          "name": "Create user",
          "event": [{"listen": "test", "script": {"exec": ["pm.test('ok', function(){});"]}}],
          "request": {
            "method": "POST",
            "url": {"raw": "{{baseUrl}}/users?expand=roles&debug=1", "query": [
              {"key": "expand", "value": "roles"}, {"key": "debug", "value": "1", "disabled": true}]},
            "header": [{"key": "X-Trace", "value": "t"}, {"key": "Accept", "value": "json", "disabled": true}],
            "body": {"mode": "raw", "raw": "{\"name\":\"Ada\"}", "options": {"raw": {"language": "json"}}},
            "auth": {"type": "noauth"}
          }
        }
      ]
    },
    {
      "name": "Upload",
      "request": {
        "method": "POST",
        "url": "{{baseUrl}}/files",
        "body": {"mode": "formdata", "formdata": [
          {"key": "title", "value": "doc", "type": "text"},
          {"key": "file", "type": "file", "src": "a.png"}]},
        "auth": {"type": "apikey", "apikey": [{"key": "key", "value": "X-Key"}, {"key": "value", "value": "{{apiKey}}"}, {"key": "in", "value": "query"}]}
      }
    },
    {
      "name": "Ping",
      "request": {"method": "GET", "url": "{{baseUrl}}/ping?x=1&y=2",
        "auth": {"type": "oauth2", "oauth2": [
          {"key": "grantType", "value": "client_credentials"}, {"key": "accessTokenUrl", "value": "https://auth/token"},
          {"key": "clientId", "value": "id"}, {"key": "clientSecret", "value": "sec"}, {"key": "addTokenTo", "value": "header"}]}}
    },
    {
      "name": "Legacy",
      "request": {"method": "GET", "url": "https://x/legacy", "auth": {"type": "awsv4"},
        "body": {"mode": "graphql", "graphql": {"query": "{ me { id } }", "variables": "{\"a\":1}"}}}
    }
  ]
}`

func TestParsePostmanStructure(t *testing.T) {
	c, err := ParsePostman([]byte(samplePostman))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Shop API" {
		t.Fatalf("name %q", c.Name)
	}
	if got, _ := c.Bru.Vars.Get("retries"); got != "3" {
		t.Fatalf("retries %q", got)
	}
	if c.Bru.Auth != "bearer" {
		t.Fatalf("collection auth %q", c.Bru.Auth)
	}
	if len(c.Folders) != 1 || c.Folders[0].Name != "Users" || c.Folders[0].Bru.Auth != "basic" {
		t.Fatalf("folders %+v", c.Folders)
	}
	if len(c.Requests) != 3 {
		t.Fatalf("top-level requests %d", len(c.Requests))
	}

	create := c.Folders[0].Requests[0]
	if create.Method != "post" || create.URL != "{{baseUrl}}/users" || create.Body != "json" || create.BodyJSON != `{"name":"Ada"}` {
		t.Fatalf("create %+v", create)
	}
	if create.Auth != "none" {
		t.Fatalf("noauth -> %q", create.Auth)
	}
	if len(create.ParamsQuery) != 2 || !create.ParamsQuery[0].Enabled || create.ParamsQuery[1].Enabled {
		t.Fatalf("query %+v", create.ParamsQuery)
	}
	if create.Headers[1].Enabled || create.Headers[0].Key != "X-Trace" {
		t.Fatalf("headers %+v", create.Headers)
	}
	if create.Meta.Seq != 1 {
		t.Fatalf("seq %d", create.Meta.Seq)
	}

	upload := c.Requests[0]
	if upload.Body != "multipart-form" || len(upload.BodyMultipart) != 1 || upload.BodyMultipart[0].Key != "title" {
		t.Fatalf("multipart %+v", upload.BodyMultipart)
	}
	if upload.Auth != "apikey" {
		t.Fatalf("auth %q", upload.Auth)
	}
	if v, _ := upload.AuthAPIKey.Get("placement"); v != "queryparams" {
		t.Fatalf("placement %q", v)
	}

	ping := c.Requests[1]
	if ping.Auth != "oauth2" {
		t.Fatalf("oauth2 %q", ping.Auth)
	}
	if v, _ := ping.AuthOAuth2.Get("access_token_url"); v != "https://auth/token" {
		t.Fatalf("token url %q", v)
	}
	if v, _ := ping.AuthOAuth2.Get("grant_type"); v != "client_credentials" {
		t.Fatalf("grant %q", v)
	}

	legacy := c.Requests[2]
	if legacy.Body != "graphql" || legacy.BodyGraphQLVars != `{"a":1}` {
		t.Fatalf("graphql %+v", legacy)
	}
	if legacy.Meta.Seq != 4 {
		t.Fatalf("seq %d", legacy.Meta.Seq)
	}
}

func TestParsePostmanWarnings(t *testing.T) {
	c, err := ParsePostman([]byte(samplePostman))
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(c.Warnings, "\n")
	for _, want := range []string{"pm.* 与 Bruno", "文件字段 \"file\"", "awsv4"} {
		if !strings.Contains(all, want) {
			t.Fatalf("warnings missing %q:\n%s", want, all)
		}
	}
}

func TestParsePostmanRejectsOthers(t *testing.T) {
	if _, err := ParsePostman([]byte(`{"log": {}}`)); err == nil {
		t.Fatal("expected error for non-Postman JSON")
	}
	if _, err := ParsePostman([]byte(`not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
