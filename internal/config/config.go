// Package config parses google-contacts-mcp settings from command-line flags
// and GCMCP_* environment variables. A flag always wins over its env var.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// envPrefix namespaces every env var so the server never picks up unrelated settings.
	envPrefix = "GCMCP_"
	// envGoogleClientSecret is env-only so the secret never shows in process listings.
	envGoogleClientSecret = envPrefix + "GOOGLE_CLIENT_SECRET"
	// envEncryptionKeys is env-only so key material never shows in process listings.
	envEncryptionKeys = envPrefix + "ENCRYPTION_KEYS"
	// envPort is the de-facto PaaS convention (Heroku, Cloud Run, Fly) for the port to bind.
	envPort = "PORT"

	// defaultListen binds loopback only, so a bare run is never exposed by accident.
	defaultListen = "127.0.0.1:8080"
	// portListenHost binds every interface when a PaaS hands us PORT: its router must reach us.
	portListenHost = "0.0.0.0"
	// defaultDBPath keeps the SQLite file under ./data, which .gitignore excludes.
	defaultDBPath = "./data/google-contacts-mcp.db"
	// defaultLogLevel logs one line per tool call without personal data.
	defaultLogLevel = "info"
	// defaultMetricsPath is the Prometheus convention.
	defaultMetricsPath = "/metrics"
	// defaultRateLimitReads is per user per minute; generous for interactive agent use.
	defaultRateLimitReads = 120
	// defaultRateLimitWrites is per user per minute; lower than reads since writes cost more Google quota.
	defaultRateLimitWrites = 30
	// minMetricsPasswordLen resists online brute force on the basic-auth metrics endpoint.
	minMetricsPasswordLen = 16

	// schemeHTTPS is required for the public base URL: it is also the OAuth issuer.
	schemeHTTPS = "https"
	// schemeHTTP is accepted only for loopback hosts, for local development.
	schemeHTTP = "http"
	// allowedDomainsSep separates entries of --allowed-domains.
	allowedDomainsSep = ","
)

// Flag names, also used to derive env var names and error messages.
const (
	flagBaseURL             = "base-url"
	flagListen              = "listen"
	flagDBPath              = "db-path"
	flagGoogleClientID      = "google-client-id"
	flagAllowedDomains      = "allowed-domains"
	flagEnableDCR           = "enable-dcr"
	flagLogLevel            = "log-level"
	flagMetrics             = "metrics"
	flagMetricsPath         = "metrics-path"
	flagMetricsListen       = "metrics-listen"
	flagMetricsUser         = "metrics-user"
	flagMetricsPassword     = "metrics-password"
	flagMetricsPasswordFile = "metrics-password-file"
	flagRateLimitReads      = "rate-limit-reads"
	flagRateLimitWrites     = "rate-limit-writes"
	flagOperatorName        = "operator-name"
	flagOperatorEmail       = "operator-email"
)

// loopbackHosts may use plain http in --base-url, for local development only.
var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true}

// logLevels lists the only accepted --log-level values; slog.Level.UnmarshalText
// would also accept offsets such as "info+2", which the spec does not allow.
var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// Config is the validated server configuration.
type Config struct {
	BaseURL            *url.URL
	Listen             string
	DBPath             string
	GoogleClientID     string
	GoogleClientSecret string
	EncryptionKeys     string
	AllowedDomains     []string
	EnableDCR          bool
	LogLevel           slog.Level
	Metrics            bool
	MetricsPath        string
	// MetricsListen is empty when metrics share the main listener.
	MetricsListen   string
	MetricsUser     string
	MetricsPassword string
	RateLimitReads  int
	RateLimitWrites int
	OperatorName    string
	OperatorEmail   string
	// Args holds the positional arguments left after the flags.
	Args []string
}

// EnvName returns the env var read for a flag: GCMCP_ plus the flag name in upper snake case.
func EnvName(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// raw holds flag values before validation.
type raw struct {
	baseURL, listen, dbPath, googleClientID, allowedDomains, logLevel string
	metricsPath, metricsListen, metricsUser                           string
	metricsPassword, metricsPasswordFile                              string
	operatorName, operatorEmail                                       string
	enableDCR, metrics                                                bool
	rateLimitReads, rateLimitWrites                                   int
}

// newFlagSet registers every flag with its default taken from the env var when
// set, so flag.Parse lets an explicit flag override the env value.
func newFlagSet(getenv func(string) string, r *raw) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet("google-contacts-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	str := func(p *string, name, def, usage string) {
		if v := getenv(EnvName(name)); v != "" {
			def = v
		}
		fs.StringVar(p, name, def, usage)
	}
	var errs []error
	boolean := func(p *bool, name string, def bool, usage string) {
		if v := getenv(EnvName(name)); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: invalid boolean %q", EnvName(name), v))
			} else {
				def = b
			}
		}
		fs.BoolVar(p, name, def, usage)
	}
	integer := func(p *int, name string, def int, usage string) {
		if v := getenv(EnvName(name)); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: invalid integer %q", EnvName(name), v))
			} else {
				def = n
			}
		}
		fs.IntVar(p, name, def, usage)
	}

	listenDefault := defaultListen
	if port := getenv(envPort); port != "" {
		listenDefault = net.JoinHostPort(portListenHost, port)
	}

	str(&r.baseURL, flagBaseURL, "", "public HTTPS URL, also the OAuth issuer (required)")
	str(&r.listen, flagListen, listenDefault, "listen address")
	str(&r.dbPath, flagDBPath, defaultDBPath, "SQLite file")
	str(&r.googleClientID, flagGoogleClientID, "", "Google OAuth client id (required)")
	str(&r.allowedDomains, flagAllowedDomains, "", "comma-separated account domains allowed to connect (empty: all)")
	boolean(&r.enableDCR, flagEnableDCR, true, "enable OAuth Dynamic Client Registration")
	str(&r.logLevel, flagLogLevel, defaultLogLevel, "debug, info, warn or error")
	boolean(&r.metrics, flagMetrics, false, "enable the Prometheus endpoint")
	str(&r.metricsPath, flagMetricsPath, defaultMetricsPath, "metrics endpoint path")
	str(&r.metricsListen, flagMetricsListen, "", "separate metrics listen address (default: main listener)")
	str(&r.metricsUser, flagMetricsUser, "", "metrics basic-auth user (required with --metrics)")
	str(&r.metricsPassword, flagMetricsPassword, "", "metrics basic-auth password, 16+ characters")
	str(&r.metricsPasswordFile, flagMetricsPasswordFile, "", "file holding the metrics basic-auth password")
	integer(&r.rateLimitReads, flagRateLimitReads, defaultRateLimitReads, "per-user reads per minute")
	integer(&r.rateLimitWrites, flagRateLimitWrites, defaultRateLimitWrites, "per-user writes per minute")
	str(&r.operatorName, flagOperatorName, "", "operator name shown on / and /privacy (required)")
	str(&r.operatorEmail, flagOperatorEmail, "", "operator contact e-mail shown on / and /privacy (required)")

	return fs, errors.Join(errs...)
}

// Usage writes the flag list and env-only secrets to w.
func Usage(w io.Writer) {
	fs, err := newFlagSet(func(string) string { return "" }, &raw{})
	if err != nil {
		_, _ = fmt.Fprintln(w, err)
		return
	}
	fs.SetOutput(w)
	fs.PrintDefaults()
	_, _ = fmt.Fprintf(w, "\nEvery flag also reads %s<FLAG_UPPER_SNAKE>; the flag wins.\n", envPrefix)
	_, _ = fmt.Fprintf(w, "Env only (required): %s, %s\n", envGoogleClientSecret, envEncryptionKeys)
}

// Subcommands that read configuration. genkey and version need none.
const (
	CmdServe      = "serve"
	CmdRekey      = "rekey"
	CmdDisconnect = "disconnect"
)

// Keys of the values a command may require; flags carry a "--" prefix in errors.
const (
	reqBaseURL            = "--" + flagBaseURL
	reqDBPath             = "--" + flagDBPath
	reqGoogleClientID     = "--" + flagGoogleClientID
	reqGoogleClientSecret = envGoogleClientSecret
	reqEncryptionKeys     = envEncryptionKeys
	reqOperatorName       = "--" + flagOperatorName
	reqOperatorEmail      = "--" + flagOperatorEmail
)

// commandSpec lists what each command needs, so offline maintenance commands
// do not demand server-only settings such as --base-url.
type commandSpec struct {
	required []string
	// nargs is the exact number of positional arguments accepted.
	nargs int
	// argsUsage names the positional arguments in error messages.
	argsUsage string
}

var commandSpecs = map[string]commandSpec{
	CmdServe: {required: []string{
		reqBaseURL, reqDBPath, reqGoogleClientID, reqGoogleClientSecret,
		reqEncryptionKeys, reqOperatorName, reqOperatorEmail,
	}},
	CmdRekey: {required: []string{reqDBPath, reqEncryptionKeys}},
	CmdDisconnect: {
		required:  []string{reqDBPath, reqEncryptionKeys, reqGoogleClientID, reqGoogleClientSecret},
		nargs:     1,
		argsUsage: "<email|google-sub>",
	},
}

// Load parses args (without the subcommand) and env into a validated Config
// for cmd. Flags must come before positional arguments, which land in
// Config.Args. It returns flag.ErrHelp when -h or --help is given.
func Load(cmd string, args []string, getenv func(string) string) (Config, error) {
	spec, ok := commandSpecs[cmd]
	if !ok {
		return Config{}, fmt.Errorf("unknown command %q", cmd)
	}
	var r raw
	fs, err := newFlagSet(getenv, &r)
	if err != nil {
		return Config{}, err
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg := Config{
		Listen:             r.listen,
		DBPath:             r.dbPath,
		GoogleClientID:     strings.TrimSpace(r.googleClientID),
		GoogleClientSecret: getenv(envGoogleClientSecret),
		EncryptionKeys:     getenv(envEncryptionKeys),
		AllowedDomains:     parseDomains(r.allowedDomains),
		EnableDCR:          r.enableDCR,
		Metrics:            r.metrics,
		MetricsPath:        r.metricsPath,
		MetricsListen:      r.metricsListen,
		MetricsUser:        r.metricsUser,
		RateLimitReads:     r.rateLimitReads,
		RateLimitWrites:    r.rateLimitWrites,
		OperatorName:       strings.TrimSpace(r.operatorName),
		OperatorEmail:      strings.TrimSpace(r.operatorEmail),
		Args:               fs.Args(),
	}

	var errs []error
	switch {
	case len(cfg.Args) == spec.nargs:
	case spec.nargs == 0:
		errs = append(errs, fmt.Errorf("%s: unexpected argument %q (flags must come first)", cmd, cfg.Args[0]))
	default:
		errs = append(errs, fmt.Errorf("%s: want exactly %d argument %s, got %d", cmd, spec.nargs, spec.argsUsage, len(cfg.Args)))
	}

	values := map[string]string{
		reqBaseURL:            r.baseURL,
		reqDBPath:             cfg.DBPath,
		reqGoogleClientID:     cfg.GoogleClientID,
		reqGoogleClientSecret: cfg.GoogleClientSecret,
		reqEncryptionKeys:     cfg.EncryptionKeys,
		reqOperatorName:       cfg.OperatorName,
		reqOperatorEmail:      cfg.OperatorEmail,
	}
	for _, name := range spec.required {
		if values[name] == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}

	if r.baseURL != "" {
		u, err := parseBaseURL(r.baseURL)
		if err != nil {
			errs = append(errs, err)
		}
		cfg.BaseURL = u
	}

	level, ok := logLevels[strings.ToLower(r.logLevel)]
	if !ok {
		errs = append(errs, fmt.Errorf("--%s: invalid value %q (want debug, info, warn or error)", flagLogLevel, r.logLevel))
	}
	cfg.LogLevel = level

	if cfg.RateLimitReads <= 0 {
		errs = append(errs, fmt.Errorf("--%s must be positive, got %d", flagRateLimitReads, cfg.RateLimitReads))
	}
	if cfg.RateLimitWrites <= 0 {
		errs = append(errs, fmt.Errorf("--%s must be positive, got %d", flagRateLimitWrites, cfg.RateLimitWrites))
	}
	if !strings.HasPrefix(cfg.MetricsPath, "/") {
		errs = append(errs, fmt.Errorf("--%s must start with /, got %q", flagMetricsPath, cfg.MetricsPath))
	}

	password, err := metricsPassword(r.metricsPassword, r.metricsPasswordFile)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.MetricsPassword = password
	if cfg.Metrics {
		errs = append(errs, validateMetricsAuth(cfg.MetricsUser, cfg.MetricsPassword)...)
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseBaseURL requires an absolute https URL, or http on a loopback host.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("--%s: %w", flagBaseURL, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("--%s: %q has no host", flagBaseURL, raw)
	}
	switch u.Scheme {
	case schemeHTTPS:
	case schemeHTTP:
		if !loopbackHosts[u.Hostname()] {
			return nil, fmt.Errorf("--%s: %q must use https (http allowed only for localhost and 127.0.0.1)", flagBaseURL, raw)
		}
	default:
		return nil, fmt.Errorf("--%s: %q must use https", flagBaseURL, raw)
	}
	return u, nil
}

// metricsPassword resolves the password from the flag or the file, never both.
func metricsPassword(value, path string) (string, error) {
	if value != "" && path != "" {
		return "", fmt.Errorf("--%s and --%s are mutually exclusive", flagMetricsPassword, flagMetricsPasswordFile)
	}
	if path == "" {
		return value, nil
	}
	// The path comes from the operator's own configuration, not from a request.
	b, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied path
	if err != nil {
		return "", fmt.Errorf("--%s: %w", flagMetricsPasswordFile, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func validateMetricsAuth(user, password string) []error {
	var errs []error
	if user == "" {
		errs = append(errs, fmt.Errorf("--%s is required with --%s", flagMetricsUser, flagMetrics))
	}
	switch {
	case password == "":
		errs = append(errs, fmt.Errorf("--%s or --%s is required with --%s", flagMetricsPassword, flagMetricsPasswordFile, flagMetrics))
	case utf8.RuneCountInString(password) < minMetricsPasswordLen:
		errs = append(errs, fmt.Errorf("--%s must be at least %d characters", flagMetricsPassword, minMetricsPasswordLen))
	}
	return errs
}

// parseDomains lowercases, trims and drops empty entries.
func parseDomains(raw string) []string {
	var out []string
	for _, d := range strings.Split(raw, allowedDomainsSep) {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	return out
}
