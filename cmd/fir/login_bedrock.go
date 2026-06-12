package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/auth"
)

// bedrockProviderID is the model-provider id Bedrock accounts are stored under.
// `fir login bedrock` is accepted as a friendly alias.
const bedrockProviderID = "amazon-bedrock"

func isBedrockAlias(id string) bool {
	return id == "bedrock" || id == "amazon-bedrock"
}

// runBedrockSetup implements `fir login bedrock` — interactive (or flag-driven)
// setup of an Amazon Bedrock account. Unlike OAuth providers, Bedrock accounts
// are credential configs, not browser flows:
//
//	--mode iam-profile|iam-keys|bearer
//	--account NAME          account label / slot id
//	--region REGION
//	--profile NAME          (iam-profile)
//	--access-key / --secret-key / --session-token   (iam-keys)
//	--token TOKEN           (bearer; seeded from $AWS_BEARER_TOKEN_BEDROCK)
//
// Missing values are prompted for interactively. Profiles are seeded from
// ~/.aws/config.
func runBedrockSetup(subArgs []string) error {
	var (
		mode     string
		account  string
		region   string
		profile  string
		access   string
		secret   string
		session  string
		token    string
		nonInter bool
	)
	for i := 0; i < len(subArgs); i++ {
		a := subArgs[i]
		next := func() string {
			if i+1 < len(subArgs) {
				i++
				return subArgs[i]
			}
			return ""
		}
		switch a {
		case "bedrock", "amazon-bedrock":
			// provider token, ignore
		case "--mode":
			mode = next()
			nonInter = true
		case "--account":
			account = next()
		case "--region":
			region = next()
		case "--profile":
			profile = next()
		case "--access-key":
			access = next()
		case "--secret-key":
			secret = next()
		case "--session-token":
			session = next()
		case "--token":
			token = next()
		case "-h", "--help":
			printBedrockHelp()
			return nil
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag for 'fir login bedrock': %s", a)
			}
		}
	}

	reader := bufio.NewReader(os.Stdin)
	prompt := func(label, def string) string {
		if def != "" {
			fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(os.Stderr, "%s: ", label)
		}
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	// Mode.
	if mode == "" {
		fmt.Fprintln(os.Stderr, "Bedrock auth mode:")
		fmt.Fprintln(os.Stderr, "  1) iam-profile  — a named ~/.aws profile (SigV4)")
		fmt.Fprintln(os.Stderr, "  2) iam-keys     — explicit AWS access/secret keys (SigV4)")
		fmt.Fprintln(os.Stderr, "  3) bearer       — Bedrock API key / bearer token")
		switch prompt("Choose 1-3", "1") {
		case "2":
			mode = "iam-keys"
		case "3":
			mode = "bearer"
		default:
			mode = "iam-profile"
		}
	}
	mode = normalizeBedrockMode(mode)
	if mode == "" {
		return fmt.Errorf("invalid --mode (want iam-profile, iam-keys, or bearer)")
	}

	cred := auth.AuthCredential{}
	switch mode {
	case "iam-profile":
		if profile == "" && !nonInter {
			profiles := readAWSProfiles()
			if len(profiles) > 0 {
				fmt.Fprintf(os.Stderr, "Available profiles: %s\n", strings.Join(profiles, ", "))
			}
			profile = prompt("AWS profile", firstOr(profiles, ""))
		}
		if profile == "" {
			return fmt.Errorf("iam-profile mode requires --profile")
		}
		if region == "" && !nonInter {
			region = prompt("AWS region", "us-east-1")
		}
		cred.Type = auth.CredentialTypeAWSIAM
		cred.Extra = map[string]any{"mode": "profile", "profile": profile}
		if region != "" {
			cred.Extra["region"] = region
		}
	case "iam-keys":
		if access == "" && !nonInter {
			access = prompt("AWS access key id", "")
		}
		if secret == "" && !nonInter {
			secret = prompt("AWS secret access key", "")
		}
		if session == "" && !nonInter {
			session = prompt("AWS session token (optional)", "")
		}
		if access == "" || secret == "" {
			return fmt.Errorf("iam-keys mode requires --access-key and --secret-key")
		}
		if region == "" && !nonInter {
			region = prompt("AWS region", "us-east-1")
		}
		cred.Type = auth.CredentialTypeAWSIAM
		cred.Extra = map[string]any{"mode": "keys", "accessKey": access, "secretKey": secret}
		if session != "" {
			cred.Extra["sessionToken"] = session
		}
		if region != "" {
			cred.Extra["region"] = region
		}
	case "bearer":
		if token == "" {
			token = os.Getenv("AWS_BEARER_TOKEN_BEDROCK")
		}
		if token == "" && !nonInter {
			token = prompt("Bedrock bearer token", "")
		}
		if token == "" {
			return fmt.Errorf("bearer mode requires --token (or $AWS_BEARER_TOKEN_BEDROCK)")
		}
		if region == "" && !nonInter {
			region = prompt("AWS region", "us-east-1")
		}
		cred.Type = auth.CredentialTypeAPIKey
		cred.Key = token
		if region != "" {
			cred.Extra = map[string]any{"region": region}
		}
	}

	if account == "" && !nonInter {
		account = prompt("Account name (label)", defaultBedrockAccountName(mode, profile, region))
	}
	if account == "" {
		account = defaultBedrockAccountName(mode, profile, region)
	}

	agentDir := resolveAgentDir()
	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	slot, err := authStorage.StoreAccount(bedrockProviderID, account, cred)
	if err != nil {
		return fmt.Errorf("store bedrock account: %w", err)
	}
	if auth.IsSlotKey(slot) {
		fmt.Fprintf(os.Stderr, "Added Bedrock account %q (slot %s, mode %s). Credentials saved.\n", account, slot, mode)
	} else {
		fmt.Fprintf(os.Stderr, "Configured default Bedrock account %q (mode %s). Credentials saved.\n", account, mode)
	}
	return nil
}

func normalizeBedrockMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "iam-profile", "profile":
		return "iam-profile"
	case "iam-keys", "keys":
		return "iam-keys"
	case "bearer", "api-key", "token":
		return "bearer"
	default:
		return ""
	}
}

func defaultBedrockAccountName(mode, profile, region string) string {
	switch mode {
	case "iam-profile":
		if profile != "" {
			return profile
		}
	}
	if region != "" {
		return region
	}
	return "default"
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// readAWSProfiles parses profile names from ~/.aws/config (and ~/.aws/credentials).
func readAWSProfiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range []string{"config", "credentials"} {
		data, err := os.ReadFile(filepath.Join(home, ".aws", name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
				continue
			}
			p := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			p = strings.TrimPrefix(p, "profile ")
			if strings.HasPrefix(p, "sso-session") || p == "" {
				continue
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

func printBedrockHelp() {
	fmt.Println("Usage: fir login bedrock [--mode iam-profile|iam-keys|bearer] [--account NAME] [--region REGION]")
	fmt.Println("                         [--profile NAME] [--access-key K --secret-key S [--session-token T]] [--token TOKEN]")
	fmt.Println()
	fmt.Println("Configures an Amazon Bedrock account. With no flags, prompts interactively.")
	fmt.Println("Multiple accounts coexist and are switchable live in the model selector.")
}
