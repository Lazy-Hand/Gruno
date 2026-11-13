package send

// Dynamic variables: {{$guid}}, {{$timestamp}} and the {{$random*}}
// family Bruno fills in at resolve time.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// dynamicVar returns the value for a {{$name}} reference. Dynamic
// values are generated per resolution, as Bruno does.
func dynamicVar(name string) (string, bool) {
	switch name {
	case "$guid", "$uuid", "$randomUUID":
		return uuid4(), true
	case "$timestamp":
		return strconv.FormatInt(time.Now().Unix(), 10), true
	case "$isoTimestamp":
		return time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), true
	case "$randomInt":
		return strconv.Itoa(randInt(1000)), true
	case "$randomBoolean":
		return strconv.FormatBool(randInt(2) == 1), true
	case "$randomFirstName":
		return pick(firstNames), true
	case "$randomLastName":
		return pick(lastNames), true
	case "$randomFullName":
		return pick(firstNames) + " " + pick(lastNames), true
	case "$randomEmail":
		return strings.ToLower(pick(firstNames)+"."+pick(lastNames)) + "@example.com", true
	case "$randomUserName":
		return strings.ToLower(pick(firstNames)) + strconv.Itoa(randInt(1000)), true
	case "$randomCity":
		return pick(cities), true
	case "$randomCountry":
		return pick(countries), true
	case "$randomPhone":
		return fmt.Sprintf("+1-%03d-%03d-%04d", randInt(1000), randInt(1000), randInt(10000)), true
	case "$randomIP":
		return fmt.Sprintf("%d.%d.%d.%d", randInt(224), randInt(256), randInt(256), randInt(256)), true
	case "$randomPassword":
		return pick(passwords) + strconv.Itoa(randInt(1000)), true
	case "$randomLorem":
		return "lorem ipsum dolor sit amet, consectetur adipiscing elit", true
	case "$randomDate":
		return time.Now().UTC().AddDate(0, 0, -randInt(365)).Format("2006-01-02"), true
	}
	return "", false
}

// DynamicNames lists the supported {{$name}} variables.
var DynamicNames = []string{
	"$guid", "$timestamp", "$isoTimestamp", "$randomInt", "$randomBoolean",
	"$randomFirstName", "$randomLastName", "$randomFullName", "$randomEmail",
	"$randomUserName", "$randomCity", "$randomCountry", "$randomPhone",
	"$randomIP", "$randomPassword", "$randomLorem", "$randomDate",
}

// DynamicVar returns a generated value for a {{$name}} reference.
func DynamicVar(name string) (string, bool) { return dynamicVar(name) }

func pick(list []string) string {
	return list[randInt(len(list))]
}

func randInt(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func uuid4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

var firstNames = []string{"Ada", "Alan", "Grace", "Linus", "Margaret", "Dennis", "Barbara", "Ken", "Radia", "Guido"}
var lastNames = []string{"Lovelace", "Turing", "Hopper", "Torvalds", "Hamilton", "Ritchie", "Liskov", "Thompson", "Perlman", "Rossum"}
var cities = []string{"Berlin", "Tokyo", "Lagos", "Lima", "Oslo", "Austin", "Seoul", "Lisbon", "Nairobi", "Toronto"}
var countries = []string{"Germany", "Japan", "Nigeria", "Peru", "Norway", "United States", "South Korea", "Portugal", "Kenya", "Canada"}
var passwords = []string{"Tr0ub4dor", "correct-horse", "s3cret", "hunter2", "p@ssw0rd"}
