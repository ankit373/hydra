// SPDX-License-Identifier: MIT

package awsauth

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Package awsauth resolves AWS credentials the way AWS's own tools do. It is
// its own package because both the executor that signs requests and the
// discovery that lists the Bedrock head have to agree on what "configured"
// means, and executor cannot be imported from provider (#867).

// profile is one resolved AWS identity: a credential, and the region it was
// configured with.
type profile struct {
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	region          string
}

// Credentials resolves a **complete** credential the way AWS's own tools do:
// the environment first, then the shared files a machine is normally configured
// through. Reading only the environment meant `aws` worked on a machine while
// Hydra reported no Bedrock head at all (#867).
//
// Complete is the point. Pairing an environment access key with a file secret
// would sign with two halves of different credentials, and the failure reads as
// a signature error rather than as the misconfiguration it is.
func Credentials() (accessKeyID, secretAccessKey, sessionToken string) {
	id, secret := firstEnv("AWS_ACCESS_KEY_ID"), firstEnv("AWS_SECRET_ACCESS_KEY")
	if id != "" && secret != "" {
		return id, secret, firstEnv("AWS_SESSION_TOKEN")
	}
	p := sharedProfile()
	return p.accessKeyID, p.secretAccessKey, p.sessionToken
}

// Configured reports whether a complete credential exists, which is what
// discovery needs to decide whether to list the head at all.
func Configured() bool {
	id, secret, _ := Credentials()
	return id != "" && secret != ""
}

// Region is the configured region, environment first, then the profile's own.
// A region is not a credential and comes from a different place, so a machine
// can have one and not the other.
func Region() string {
	if r := firstEnv("AWS_REGION", "AWS_DEFAULT_REGION"); r != "" {
		return r
	}
	return sharedProfile().region
}

// sharedProfile reads $AWS_PROFILE, or `default`, out of the credentials and
// config files. The credentials file wins where both name a key, which is the
// precedence the SDKs document.
func sharedProfile() profile {
	name := firstNonEmpty(firstEnv("AWS_PROFILE"), "default")
	merged := iniSection(credentialsFile(), name, false)
	for k, v := range iniSection(configFile(), name, true) {
		if _, ok := merged[k]; !ok {
			merged[k] = v
		}
	}

	p := profile{region: merged["region"]}
	// A credential that has to be fetched rather than read is out of scope: a
	// role, an SSO session or a credential_process each needs a token
	// exchange, and static keys sitting beside one are a different identity
	// from the one the profile stands for.
	for _, fetched := range []string{"role_arn", "credential_process", "sso_start_url", "sso_session"} {
		if merged[fetched] != "" {
			return p
		}
	}
	p.accessKeyID = merged["aws_access_key_id"]
	p.secretAccessKey = merged["aws_secret_access_key"]
	p.sessionToken = merged["aws_session_token"]
	return p
}

// iniSection reads one profile's settings. The config file prefixes every
// profile but `default` with "profile ", which is the one difference in shape
// between the two files.
func iniSection(path, name string, configStyle bool) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return map[string]string{}
	}
	defer f.Close()

	want := name
	if configStyle && name != "default" {
		want = "profile " + name
	}

	out := map[string]string{}
	inSection := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = strings.TrimSpace(line[1:len(line)-1]) == want
			continue
		}
		if !inSection {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return out
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func credentialsFile() string {
	if p := firstEnv("AWS_SHARED_CREDENTIALS_FILE"); p != "" {
		return p
	}
	return homeFile("credentials")
}

func configFile() string {
	if p := firstEnv("AWS_CONFIG_FILE"); p != "" {
		return p
	}
	return homeFile("config")
}

func homeFile(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aws", name)
}
