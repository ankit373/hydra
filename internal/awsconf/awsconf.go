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
}

// Usable reports whether these can actually sign a request. A profile carrying
// only role_arn/source_profile resolves to no keys and must not read as one:
// assuming a role needs a token exchange, not a file read.
func (c Creds) Usable() bool {
	return c.AccessKeyID != "" && c.SecretAccessKey != ""
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
	// Keys come from credentials, region from config. A key set is taken whole
	// rather than field by field, or an id from one source could be paired with
	// a secret from another and sign nothing.
	if !c.Usable() {
		if f := section(credentialsPath(), profile); f != nil {
			c.AccessKeyID = f["aws_access_key_id"]
			c.SecretAccessKey = f["aws_secret_access_key"]
			c.SessionToken = f["aws_session_token"]
		}
	}
	if c.Region == "" {
		// ~/.aws/config prefixes every non-default profile with "profile ",
		// where ~/.aws/credentials does not. Both spellings are tried because
		// a file written by hand often omits it.
		for _, name := range configSectionNames(profile) {
			if f := section(configPath(), name); f != nil && f["region"] != "" {
				c.Region = f["region"]
				break
			}
		}
	}
	return c
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
