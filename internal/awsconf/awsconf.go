// SPDX-License-Identifier: MIT

// Package awsconf resolves AWS credentials and region the way every other AWS
// tool does: environment first, then the shared config files.
//
// Hydra read the environment only, so a machine where `aws sts
// get-caller-identity` works showed no Bedrock head at all, which reads as "you
// have no Bedrock" rather than "Hydra looks in one of the places" (#867).
package awsconf

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Creds is a resolved credential and region. Region is independent: it lives in
// config where the keys live in credentials, and either can be absent.
type Creds struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string

	// Deferred names a directive the profile carries that requires a token
	// exchange Hydra does not perform (role_arn, sso_session,
	// credential_process). Empty when the keys stand on their own.
	Deferred string
}

// deferredKeys are the directives that mean "these keys are not the identity".
// Where a profile declares one, any static keys beside it are the credential
// used to *obtain* the identity, so signing with them acts as the base
// principal instead of the role, which is a wrong answer rather than a
// degraded one (#890).
var deferredKeys = []string{"role_arn", "sso_session", "credential_process", "sso_start_url"}

// Usable reports whether these can actually sign a request *as the identity the
// profile stands for*. A profile that defers is not usable at any strength of
// static key: Hydra performs no token exchange, so the honest answer is no
// credential rather than the wrong one.
func (c Creds) Usable() bool {
	return c.Deferred == "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// Resolve reads the environment, then the profile named by AWS_PROFILE (or
// "default") out of the shared files. Each field falls back independently, so
// AWS_REGION with file-based keys works, as does the reverse.
func Resolve() Creds {
	c := Creds{
		AccessKeyID:     env("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: env("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    env("AWS_SESSION_TOKEN"),
		Region:          env("AWS_REGION", "AWS_DEFAULT_REGION"),
	}
	if c.Usable() && c.Region != "" {
		return c
	}

	profile := env("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	creds := section(credentialsPath(), profile)
	// ~/.aws/config prefixes every non-default profile with "profile ", where
	// ~/.aws/credentials does not. Both spellings are tried because a file
	// written by hand often omits it.
	var conf map[string]string
	for _, name := range configSectionNames(profile) {
		if f := section(configPath(), name); f != nil {
			conf = f
			break
		}
	}

	// Read from both files, because role_arn and sso_session conventionally
	// live in config while the static keys live in credentials. Checking only
	// the credentials file would miss the ordinary assume-role setup entirely.
	deferred := firstNonEmptyOf(deferredBy(creds), deferredBy(conf))

	// A key set is taken whole rather than field by field, or an id from one
	// source could be paired with a secret from another and sign nothing.
	if !c.Usable() && creds != nil {
		c.AccessKeyID = creds["aws_access_key_id"]
		c.SecretAccessKey = creds["aws_secret_access_key"]
		c.SessionToken = creds["aws_session_token"]
	}
	// Applies to environment keys too: a profile that defers is a statement
	// about which identity this run is meant to act as, and AWS_PROFILE naming
	// such a profile is not satisfied by whatever keys happen to be exported.
	//
	// The keys are cleared rather than merely flagged, so a caller that reads
	// AccessKeyID without consulting Usable cannot sign as the base principal
	// by accident. Leaving a usable-looking key on a struct that means "not
	// usable" is the footgun this whole fix is about.
	if deferred != "" {
		c.AccessKeyID, c.SecretAccessKey, c.SessionToken = "", "", ""
	}
	c.Deferred = deferred

	if c.Region == "" {
		// A region is configuration, not a credential, so it is read from a
		// deferring profile as well: the head is unroutable for want of an
		// identity, and reporting a second missing thing would be noise.
		c.Region = conf["region"]
	}
	return c
}

// deferredBy names the first directive in f that means "these keys are not the
// identity", or "" when there is none.
func deferredBy(f map[string]string) string {
	for _, k := range deferredKeys {
		if strings.TrimSpace(f[k]) != "" {
			return k
		}
	}
	return ""
}

func firstNonEmptyOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func configSectionNames(profile string) []string {
	if profile == "default" {
		return []string{"default", "profile default"}
	}
	return []string{"profile " + profile, profile}
}

func env(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func credentialsPath() string {
	if p := env("AWS_SHARED_CREDENTIALS_FILE"); p != "" {
		return p
	}
	return filepath.Join(home(), ".aws", "credentials")
}

func configPath() string {
	if p := env("AWS_CONFIG_FILE"); p != "" {
		return p
	}
	return filepath.Join(home(), ".aws", "config")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// section returns one INI section's keys, or nil when the file or section is
// absent. An unreadable file is absence: a credential that cannot be read is
// not a credential, and failing discovery over it would hide every other head.
func section(path, want string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var in bool
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if in {
				break // the next header ends the one we wanted
			}
			in = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), want)
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if !in && len(out) == 0 {
		return nil
	}
	return out
}
