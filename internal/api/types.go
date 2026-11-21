package api

import (
	"encoding/json"
	"time"
)

// The shapes below mirror ../lyrelog/web/src/lib/api.ts. JSON output passes the server's own
// objects through untouched; these are for human output.

type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Organization struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	Plan      string     `json:"plan"`
	PlanUntil *time.Time `json:"plan_until"`
}

type Me struct {
	User          User           `json:"user"`
	Organizations []Organization `json:"organizations"`
	Admin         bool           `json:"admin"`
}

type Project struct {
	ID                 string          `json:"id"`
	OrganizationID     int64           `json:"organization_id"`
	Slug               string          `json:"slug"`
	Name               string          `json:"name"`
	RetentionDays      int             `json:"retention_days"`
	MaxRetentionDays   int             `json:"max_retention_days"`
	CreatedAt          time.Time       `json:"created_at"`
	Role               string          `json:"role"`
	Platform           string          `json:"platform"`
	StoreApp           json.RawMessage `json:"store_app"`
	PersonOne          string          `json:"person_one"`
	PersonMany         string          `json:"person_many"`
	DefaultEnvironment string          `json:"default_environment"`
	Platforms          []string        `json:"platforms"`
	PlatformDetails    json.RawMessage `json:"platform_details"`
	AttestAppIDs       []string        `json:"attest_app_ids"`
	RequireVerified    []string        `json:"require_verified"`
}

type HourCount struct {
	Events int64 `json:"events"`
	Errors int64 `json:"errors"`
}

type ProjectSummary struct {
	Project
	Events24h        int64       `json:"events_24h"`
	Errors24h        int64       `json:"errors_24h"`
	Users24h         int64       `json:"users_24h"`
	LastEvent        *int64      `json:"last_event"`
	Hours            []HourCount `json:"hours"`
	StatsUnavailable bool        `json:"stats_unavailable"`
}

type Event struct {
	EventID     string          `json:"event_id"`
	TS          int64           `json:"ts"`
	Level       string          `json:"level"`
	Category    string          `json:"category"`
	Message     string          `json:"message"`
	UserID      string          `json:"user_id"`
	InstallID   string          `json:"install_id"`
	SessionID   string          `json:"session_id"`
	AppID       string          `json:"app_id"`
	AppVersion  string          `json:"app_version"`
	AppBuild    string          `json:"app_build"`
	OSName      string          `json:"os_name"`
	OSVersion   string          `json:"os_version"`
	DeviceModel string          `json:"device_model"`
	Locale      string          `json:"locale"`
	Country     string          `json:"country"`
	Environment string          `json:"environment"`
	Platform    string          `json:"platform"`
	VerifiedBy  string          `json:"verified_by"`
	Attrs       json.RawMessage `json:"attrs"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	// ReceivedAt: when Lyrelog received it (Unix ms), which can be long after TS.
	ReceivedAt int64 `json:"received_at"`
}

type EventPage struct {
	Events     []json.RawMessage `json:"events"`
	NextCursor *string           `json:"next_cursor"`
}

type APIKey struct {
	ID         string     `json:"id"`
	Scope      string     `json:"scope"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type Issue struct {
	Issue     string   `json:"issue"`
	Area      string   `json:"area"`
	Name      string   `json:"name"`
	Count     int64    `json:"count"`
	Players   int64    `json:"players"`
	Sessions  int64    `json:"sessions"`
	Previous  int64    `json:"previous"`
	FirstSeen int64    `json:"first_seen"`
	LastSeen  int64    `json:"last_seen"`
	Worst     string   `json:"worst"`
	Versions  []string `json:"versions"`
	Trend     []int64  `json:"trend"`
}

type Session struct {
	ID         string `json:"sid"`
	Who        string `json:"who"`
	Started    int64  `json:"started"`
	Ended      int64  `json:"ended"`
	DurationMS int64  `json:"duration_ms"`
	Events     int64  `json:"events"`
	Errors     int64  `json:"errors"`
	FirstEvent string `json:"first_event"`
	LastEvent  string `json:"last_event"`
	FirstError string `json:"first_error"`
	Device     string `json:"on_device"`
	Version    string `json:"on_version"`
	Place      string `json:"place"`
}

type Person struct {
	Player    string `json:"player"`
	Events    int64  `json:"events"`
	Sessions  int64  `json:"sessions"`
	Errors    int64  `json:"errors"`
	LastSeen  int64  `json:"last_seen"`
	FirstSeen int64  `json:"first_seen"`
	Device    string `json:"last_device"`
	Version   string `json:"last_version"`
	OS        string `json:"last_os"`
	OSName    string `json:"last_os_name"`
	Country   string `json:"last_country"`
}

type Release struct {
	Version       string `json:"version"`
	Events        int64  `json:"events"`
	Players       int64  `json:"players"`
	Sessions      int64  `json:"sessions"`
	ErrorSessions int64  `json:"error_sessions"`
	Errors        int64  `json:"errors"`
	Criticals     int64  `json:"criticals"`
	FirstSeen     int64  `json:"first_seen"`
	LastSeen      int64  `json:"last_seen"`
}

type Usage struct {
	Organization string     `json:"organization"`
	Plan         string     `json:"plan"`
	PlanUntil    *time.Time `json:"plan_until"`
	Limits       struct {
		HardEvents     int64 `json:"hard_events"`
		IncludedEvents int64 `json:"included_events"`
	} `json:"limits"`
	Events     int64     `json:"events"`
	MonthStart time.Time `json:"month_start"`
	NextMonth  time.Time `json:"next_month"`
	Projects   []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Events int64  `json:"events"`
	} `json:"projects"`
	Days []struct {
		Day    string `json:"day"`
		Events int64  `json:"events"`
	} `json:"days"`
	DaysInMonth int `json:"days_in_month"`
}

type Member struct {
	UserID    int64      `json:"user_id"`
	Email     string     `json:"email"`
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	Since     time.Time  `json:"since"`
	LastLogin *time.Time `json:"last_login_at"`
}

type Invitation struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Email     string    `json:"email"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type OrganizationPeople struct {
	ID           int64        `json:"id"`
	Organization string       `json:"organization"`
	Role         string       `json:"role"`
	Members      []Member     `json:"members"`
	Invites      []Invitation `json:"invites"`
}

type TrendSeries struct {
	Event  string   `json:"event"`
	Group  string   `json:"group"`
	Total  *float64 `json:"total"`
	Hits   *int64   `json:"hits"`
	Tries  *int64   `json:"tries"`
	Points []struct {
		T int64    `json:"t"`
		V *float64 `json:"v"`
	} `json:"points"`
}

type FunnelStep struct {
	Event    string   `json:"event"`
	People   int64    `json:"people"`
	MedianMS *float64 `json:"median_ms"`
}

type SavedView struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
}

type EndUser struct {
	Events int64 `json:"events"`
	First  int64 `json:"first"`
	Last   int64 `json:"last"`
}

type Account struct {
	User       User `json:"user"`
	Identities []struct {
		Provider   string     `json:"provider"`
		Email      string     `json:"email"`
		LastUsedAt *time.Time `json:"last_used_at"`
	} `json:"identities"`
	Passkeys []struct {
		Name       string     `json:"name"`
		LastUsedAt *time.Time `json:"last_used_at"`
	} `json:"passkeys"`
	TwoFactor         bool  `json:"two_factor"`
	RecoveryCodesLeft int   `json:"recovery_codes_left"`
	EmailVerified     *bool `json:"email_verified"`
}

// TokenGrant is POST /api/cli/token's answer.
type TokenGrant struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	User      User   `json:"user"`
}
