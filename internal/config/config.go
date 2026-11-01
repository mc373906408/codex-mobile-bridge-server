package config

import (
	"encoding/base64"
	"errors"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Mode, Listen, Origin, DatabaseURL string
	DataKey                           []byte
	AdminLogin, AdminPassword         string
	Development                       bool
	TrustedProxies                    []netip.Prefix
	MaxGateways, MaxInstallations     int
	MaxConnections, MaxPerGateway     int
	BytesPerSecond                    int
	AccessTTL, RefreshTTL             time.Duration
	SMTP                              SMTP
	Umeng                             Umeng
	APNS                              APNS
}

type SMTP struct {
	Address, Host, User, Password, From, TLS string
}

type Umeng struct {
	AppKey, MasterSecret string
	Production           bool
}

type APNS struct {
	TeamID, KeyID, Topic, PrivateKey string
	Sandbox                          bool
}

func (c Config) Paid() bool         { return c.Mode == "hosted" }
func (c Config) MailEnabled() bool  { return c.SMTP.Address != "" }
func (c Config) UmengEnabled() bool { return c.Umeng.AppKey != "" && c.Umeng.MasterSecret != "" }
func (c Config) APNSEnabled() bool {
	return c.APNS.TeamID != "" && c.APNS.KeyID != "" && c.APNS.Topic != "" && c.APNS.PrivateKey != ""
}

func secret(name string) (string, error) {
	value, file := os.Getenv(name), os.Getenv(name+"_FILE")
	if value != "" && file != "" {
		return "", errors.New(name + " and " + name + "_FILE cannot both be set")
	}
	if file != "" {
		bytes, err := os.ReadFile(file)
		if err != nil {
			return "", errors.New("cannot read " + name + "_FILE")
		}
		value = strings.TrimSpace(string(bytes))
		if value == "" {
			return "", errors.New(name + "_FILE must not be empty")
		}
	}
	return value, nil
}

func Load() (Config, error) {
	c := Config{
		Mode: env("BRIDGE_MODE", "selfhost"), Listen: env("BRIDGE_LISTEN", "127.0.0.1:8080"),
		Origin:      env("BRIDGE_ORIGIN", "http://127.0.0.1:8080"),
		Development: os.Getenv("BRIDGE_DEVELOPMENT") == "true",
		AdminLogin:  os.Getenv("BRIDGE_ADMIN_LOGIN"),
		MaxGateways: 8, MaxInstallations: 16, MaxConnections: 1024, MaxPerGateway: 16,
		BytesPerSecond: 1024 * 1024, AccessTTL: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour,
		SMTP: SMTP{Address: os.Getenv("SMTP_ADDRESS"), Host: os.Getenv("SMTP_HOST"),
			User: os.Getenv("SMTP_USER"), From: os.Getenv("SMTP_FROM"), TLS: env("SMTP_TLS", "starttls")},
		Umeng: Umeng{AppKey: os.Getenv("UMENG_APP_KEY"), Production: os.Getenv("UMENG_PRODUCTION") != "false"},
		APNS: APNS{TeamID: os.Getenv("APNS_TEAM_ID"), KeyID: os.Getenv("APNS_KEY_ID"),
			Topic: os.Getenv("APNS_TOPIC"), Sandbox: os.Getenv("APNS_SANDBOX") == "true"},
	}
	var err error
	if os.Getenv("UMENG_MESSAGE_SECRET") != "" || os.Getenv("UMENG_MESSAGE_SECRET_FILE") != "" {
		return c, errors.New("use UMENG_APP_MASTER_SECRET for server HTTP signing; Message Secret belongs only in the Android build")
	}
	for _, raw := range strings.Split(os.Getenv("BRIDGE_TRUSTED_PROXIES"), ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			p, e := netip.ParsePrefix(raw)
			if e != nil {
				return c, errors.New("invalid trusted proxy CIDR")
			}
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}
	for name, out := range map[string]*string{"DATABASE_URL": &c.DatabaseURL, "BRIDGE_ADMIN_PASSWORD": &c.AdminPassword,
		"SMTP_PASSWORD": &c.SMTP.Password, "UMENG_APP_MASTER_SECRET": &c.Umeng.MasterSecret, "APNS_PRIVATE_KEY": &c.APNS.PrivateKey} {
		*out, err = secret(name)
		if err != nil {
			return c, err
		}
	}
	if c.AdminLogin == "" && c.AdminPassword == "" && os.Getenv("BRIDGE_ADMIN_PASSWORD_FILE") == "" {
		c.AdminLogin, c.AdminPassword = "admin", "admin"
	}
	key, err := secret("BRIDGE_DATA_KEY")
	if err != nil {
		return c, err
	}
	c.DataKey, err = base64.StdEncoding.DecodeString(key)
	if err != nil || len(c.DataKey) != 32 {
		return c, errors.New("BRIDGE_DATA_KEY must be a base64 encoded 32-byte key")
	}
	for name, out := range map[string]*int{"BRIDGE_MAX_GATEWAYS": &c.MaxGateways, "BRIDGE_MAX_INSTALLATIONS": &c.MaxInstallations,
		"BRIDGE_MAX_CONNECTIONS": &c.MaxConnections, "BRIDGE_MAX_PER_GATEWAY": &c.MaxPerGateway,
		"BRIDGE_BYTES_PER_SECOND": &c.BytesPerSecond} {
		if raw := os.Getenv(name); raw != "" {
			*out, err = strconv.Atoi(raw)
			if err != nil || *out < 1 {
				return c, errors.New("invalid " + name)
			}
		}
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Mode != "hosted" && c.Mode != "selfhost" {
		return errors.New("BRIDGE_MODE must be hosted or selfhost")
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("BRIDGE_ORIGIN must be an exact origin without a path")
	}
	if u.Host != strings.ToLower(u.Host) || (u.Scheme == "https" && u.Port() == "443") || len(c.Origin) > 2048 {
		return errors.New("BRIDGE_ORIGIN must use a canonical lower-case authority")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != u.Port() {
			return errors.New("BRIDGE_ORIGIN must use a canonical port")
		}
	}
	if c.Development {
		ip := net.ParseIP(u.Hostname())
		listen, _, e := net.SplitHostPort(c.Listen)
		lip := net.ParseIP(listen)
		if e != nil || ip == nil || !ip.IsLoopback() || lip == nil || !lip.IsLoopback() || c.Paid() {
			return errors.New("development mode is restricted to loopback selfhost")
		}
	} else if u.Scheme != "https" {
		return errors.New("HTTPS is required outside loopback development")
	}
	if len(c.DataKey) != 32 || c.DatabaseURL == "" {
		return errors.New("database and data encryption key are required")
	}
	if (c.AdminLogin == "") != (c.AdminPassword == "") {
		return errors.New("bootstrap admin login and password must be configured together")
	}
	defaultAdministrator := c.AdminLogin == "admin" && c.AdminPassword == "admin"
	if c.AdminPassword != "" && !defaultAdministrator && (len(c.AdminPassword) < 12 || len(c.AdminPassword) > 256) {
		return errors.New("bootstrap admin password must contain 12 to 256 bytes")
	}
	if c.Paid() && !c.MailEnabled() {
		return errors.New("hosted mode requires SMTP")
	}
	if c.MailEnabled() {
		if c.SMTP.Host == "" || (c.SMTP.TLS != "starttls" && c.SMTP.TLS != "implicit") {
			return errors.New("SMTP requires a verification host and starttls or implicit TLS")
		}
		if _, err := mail.ParseAddress(c.SMTP.From); err != nil {
			return errors.New("invalid SMTP_FROM")
		}
	}
	if (c.Umeng.AppKey == "") != (c.Umeng.MasterSecret == "") {
		return errors.New("Umeng credentials must be configured together")
	}
	apns := 0
	for _, s := range []string{c.APNS.TeamID, c.APNS.KeyID, c.APNS.Topic, c.APNS.PrivateKey} {
		if s != "" {
			apns++
		}
	}
	if apns != 0 && apns != 4 {
		return errors.New("all APNs credentials must be configured together")
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
