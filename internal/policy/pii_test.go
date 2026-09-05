// SPDX-License-Identifier: MIT

package policy

import "testing"

// The eight strings #169 reported, pinned as fixed expectations. Four were
// secrets that reached a cloud provider; four were ordinary developer text that
// was needlessly forced local-only.
func TestContainsPII_IssueCorpus(t *testing.T) {
	mustDetect := []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc",
	}
	mustClear := []string{
		"upgrade the driver to version 1.2.3.4 and rebuild",
		"the server is listening on 127.0.0.1",
		"look up order 123456789 in the orders table",
		"set the config: token: 4096",
	}
	for _, s := range mustDetect {
		if !ContainsPII(Request{Prompt: s}) {
			t.Errorf("MISSED (would reach a cloud provider): %q", s)
		}
	}
	for _, s := range mustClear {
		if ContainsPII(Request{Prompt: s}) {
			t.Errorf("FALSE POSITIVE (needlessly local-only): %q", s)
		}
	}
}

func TestContainsPII_Detects(t *testing.T) {
	cases := map[string]string{
		"pem rsa":            "here is the key\n-----BEGIN RSA PRIVATE KEY-----\nMIIE...",
		"pem openssh":        "-----BEGIN OPENSSH PRIVATE KEY-----",
		"pem plain":          "-----BEGIN PRIVATE KEY-----",
		"pem ec":             "-----BEGIN EC PRIVATE KEY-----",
		"aws akia":           "export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
		"aws asia":           "ASIAY34FZKBOKMUTVV7A",
		"github pat":         "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"github oauth":       "gho_16C7e42F292c6912E7710c838347Ae178B4a",
		"slack bot":          "xoxb-123456789012-abcdefghijkl",
		"openai":             "sk-abcdefghijklmnopqrstuvwxyz012345",
		"google":             "AIzaSyD-1234567890abcdefghijklmnopqrstu",
		"jwt bare":           "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc",
		"bearer header":      "Authorization: Bearer sometokenvalue",
		"basic header":       "authorization: Basic dXNlcjpwYXNz",
		"email":              "mail alice.smith@example.co.uk about it",
		"ssn dashed":         "his ssn is 123-45-6789",
		"ssn dotted":         "123.45.6789",
		"ssn spaced":         "123 45 6789",
		"real visa":          "card 4111 1111 1111 1111 on file",
		"real mastercard":    "5500005555555559",
		"password assign":    "password: hunter2",
		"api key assign":     "api_key = s3cr3tValue123",
		"secret quoted":      `secret: "abcdefghijk"`,
		"private key assign": "private_key=MIIEpAIBAAKCAQEA",
		"public ip":          "connect to 8.8.8.8 for dns",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if !ContainsPII(Request{Prompt: in}) {
				t.Errorf("not detected: %q", in)
			}
		})
	}
}

func TestContainsPII_Clears(t *testing.T) {
	cases := map[string]string{
		"semver 4 part":      "upgrade the driver to version 1.2.3.4 and rebuild",
		"semver lowercase v": "bump to v10.0.0.1",
		"loopback":           "the server is listening on 127.0.0.1",
		"private 192":        "the router is at 192.168.1.1",
		"private 10":         "the pod ip is 10.0.0.5",
		"private 172":        "gateway 172.16.254.1",
		"link local":         "fell back to 169.254.0.1",
		"unspecified":        "bind to 0.0.0.0 for all interfaces",
		"broadcast":          "send to 255.255.255.255",
		"octet overflow":     "the build id is 999.888.777.666",
		"longer dotted run":  "the oid is 1.2.3.4.5 in the mib",
		"order number":       "look up order 123456789 in the orders table",
		"token is a size":    "set the config: token: 4096",
		"timeout is a size":  "secret = 30",
		"placeholder":        "password: changeme",
		"empty placeholder":  "api_key: none",
		"16 digits not luhn": "trace id 1234567812345678 in the log",
		"plain prose":        "refactor the dispatcher to use a context",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if ContainsPII(Request{Prompt: in}) {
				t.Errorf("false positive: %q", in)
			}
		})
	}
}

// Luhn is what separates a card number from any other 16-digit run, so pin it
// directly rather than only through ContainsPII.
func TestValidLuhn(t *testing.T) {
	valid := []string{"4111111111111111", "5500 0055 5555 5559", "4111-1111-1111-1111"}
	invalid := []string{"4111111111111112", "1234567812345678", "1111111111111111"}
	for _, s := range valid {
		if !validLuhn(s, []int{0, len(s)}) {
			t.Errorf("valid card rejected: %q", s)
		}
	}
	for _, s := range invalid {
		if validLuhn(s, []int{0, len(s)}) {
			t.Errorf("non-card accepted: %q", s)
		}
	}
}

// A private address is not PII and appears constantly in developer prompts;
// a public one plausibly identifies someone.
func TestValidPublicIP_RangeClassification(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "203.0.113.7"}
	nonPublic := []string{
		"0.0.0.0", "10.1.2.3", "127.0.0.1", "169.254.1.1",
		"172.16.0.1", "172.31.255.255", "192.168.0.1", "224.0.0.1", "255.255.255.255",
	}
	for _, s := range public {
		if !ContainsPII(Request{Prompt: "host " + s + " here"}) {
			t.Errorf("public IP not detected: %q", s)
		}
	}
	for _, s := range nonPublic {
		if ContainsPII(Request{Prompt: "host " + s + " here"}) {
			t.Errorf("non-public IP treated as PII: %q", s)
		}
	}
	// 172.15 and 172.32 are outside the private block and stay public.
	for _, s := range []string{"172.15.0.1", "172.32.0.1"} {
		if !ContainsPII(Request{Prompt: "host " + s + " here"}) {
			t.Errorf("%q is outside 172.16/12 and should count as public", s)
		}
	}
}

// A known, deliberate false positive: a version directory inside a path reads as
// a public address. Suppressing dotted quads after "/" would also suppress real
// addresses in URLs like http://8.8.8.8/foo, and that trade runs the wrong way,
// a false positive costs only local routing, a false negative leaks a secret.
//
// Pinned so the behaviour is a decision on record rather than an accident, and
// so anyone tightening it sees what they would break.
func TestContainsPII_KnownFalsePositive_VersionInPath(t *testing.T) {
	if !ContainsPII(Request{Prompt: "see /opt/app/1.2.3.4/bin"}) {
		t.Skip("version-in-path no longer reads as an IP, if that was deliberate, " +
			"check http://8.8.8.8/foo is still detected and delete this test")
	}
	if !ContainsPII(Request{Prompt: "fetch http://8.8.8.8/foo"}) {
		t.Error("an address inside a URL must still be detected")
	}
}
