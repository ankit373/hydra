// SPDX-License-Identifier: MIT

package policy

import (
	"bufio"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type secretFixture struct {
	detector, text string
	values         []string // the random parts, which must not survive Redact
}

var fixtureSlot = regexp.MustCompile(`\[\[([AaUb]):(\d+)\]\]`)

var fixtureAlphabets = map[string]string{
	"A": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
	"a": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	"U": "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
	"b": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789/+",
}

// loadSecretFixtures fills testdata/secrets.txt from a fixed seed, so the
// repo holds no string a secret scanner would flag.
func loadSecretFixtures(t *testing.T) []secretFixture {
	t.Helper()
	f, err := os.Open("testdata/secrets.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rng := rand.New(rand.NewPCG(1564, 1564))
	var out []secretFixture
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, tmpl, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("fixture line has no tab: %q", line)
		}
		fx := secretFixture{detector: name}
		fx.text = fixtureSlot.ReplaceAllStringFunc(tmpl, func(slot string) string {
			m := fixtureSlot.FindStringSubmatch(slot)
			n, _ := strconv.Atoi(m[2])
			abc := fixtureAlphabets[m[1]]
			b := make([]byte, n)
			for i := range b {
				b[i] = abc[rng.IntN(len(abc))]
			}
			fx.values = append(fx.values, string(b))
			return string(b)
		})
		out = append(out, fx)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every fixture is caught by the detector it names, and Redact leaves none of
// its secret behind (#1564). Failures print the detector, never the value.
func TestRedact_CatchesEverySecretFixture(t *testing.T) {
	fixtures := loadSecretFixtures(t)
	if len(fixtures) == 0 {
		t.Fatal("no fixtures loaded, so this measures nothing")
	}
	for i, fx := range fixtures {
		if !has(DetectPII(Request{Prompt: fx.text}), fx.detector) {
			t.Errorf("fixture %d: %s not detected", i+1, fx.detector)
		}
		redacted, _ := Redact(fx.text)
		for _, v := range fx.values {
			if strings.Contains(redacted, v) {
				t.Errorf("fixture %d: part of the %s survived redaction", i+1, fx.detector)
			}
		}
		if left := DetectPII(Request{Prompt: redacted}); len(left) > 0 {
			t.Errorf("fixture %d: redacted text still trips %v", i+1, left)
		}
	}
}

// The wider key and value matching must not turn ordinary config and code
// into secrets, since every hit also forces local-only routing.
func TestSecretFormats_LeaveLookalikesAlone(t *testing.T) {
	for _, in := range []string{
		"commit 3f786850e387550fdab836ed7e6dc881de23001b fixed it",
		"primary_key=True",
		"sort_key = created_at",
		"foreign_key: user_id",
		"max_tokens = 4096",
		`api_key = os.getenv("OPENAI_API_KEY")`,
		`SecretAccessKey: env("AWS_SECRET_ACCESS_KEY"),`,
		"OPENAI_API_KEY=${OPENAI_API_KEY}",
		"export OPENAI_API_KEY=sk-...",
		"open internal/policy/testdata/presidio2sample now",
		"skip_test_cases_for_now",
		"PWD=/home/sam/projects",
	} {
		if got := DetectPII(Request{Prompt: in}); len(got) > 0 {
			t.Errorf("DetectPII(%q) = %v, want nothing", in, got)
		}
	}
}

// A base64 blob longer than an AWS secret is read as one blob, not a key.
func TestAWSSecret_SkipsLongerBase64Runs(t *testing.T) {
	blob := strings.Repeat("Zm9vYmFyYmF6cXV4MTIz", 4)
	if got := DetectPII(Request{Prompt: "data: " + blob}); has(got, "aws secret key") {
		t.Errorf("an 80-character base64 run read as an AWS secret")
	}
}
