package discord

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

// Embed represents a Discord embed structure
type Embed struct {
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	Color       int     `json:"color,omitempty"`
	Timestamp   string  `json:"timestamp,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
}

// Field represents a field in a Discord embed
type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// WebhookPayload represents the Discord webhook payload
type WebhookPayload struct {
	Content   string  `json:"content,omitempty"`
	Username  string  `json:"username,omitempty"`
	AvatarURL string  `json:"avatar_url,omitempty"`
	Embeds    []Embed `json:"embeds,omitempty"`
}

// NotificationType represents the type of notification
type NotificationType int

const (
	NotifyFollow NotificationType = iota
	NotifyUnfollow
	NotifyPendingRequest
	NotifyAccepted
	NotifyRejected
	NotifyBlocked
)

// Colors for different notification types
const (
	ColorGreen  = 0x2ECC71 // Follow accepted
	ColorRed    = 0xE74C3C // Unfollow
	ColorYellow = 0xF1C40F // Pending request
	ColorBlue   = 0x3498DB // Accepted by admin
	ColorGray   = 0x95A5A6 // Rejected by admin
	ColorOrange = 0xE67E22 // Blocked server attempted
)

var webhookURL string
var serviceName string
var serviceIconURL string

// Initialize sets up the Discord notifier
func Initialize(url, name, iconURL string) {
	webhookURL = url
	serviceName = name
	serviceIconURL = iconURL
	if webhookURL != "" {
		logrus.Info("Discord notifications enabled")
	}
}

// IsEnabled returns whether Discord notifications are enabled
func IsEnabled() bool {
	return webhookURL != ""
}

// SendNotification sends a notification to Discord
func SendNotification(notifyType NotificationType, domain, actorID string) {
	if !IsEnabled() {
		return
	}

	var embed Embed
	embed.Timestamp = time.Now().UTC().Format(time.RFC3339)
	embed.Fields = []Field{
		{Name: "Domain", Value: domain, Inline: true},
		{Name: "Actor", Value: actorID, Inline: false},
	}

	switch notifyType {
	case NotifyFollow:
		embed.Title = "✅ New Server Registered"
		embed.Description = "A new server has joined the relay."
		embed.Color = ColorGreen
	case NotifyUnfollow:
		embed.Title = "❌ Server Unregistered"
		embed.Description = "A server has left the relay."
		embed.Color = ColorRed
	case NotifyPendingRequest:
		embed.Title = "⏳ Pending Follow Request"
		embed.Description = "A new server is requesting to join the relay (manual approval required)."
		embed.Color = ColorYellow
	case NotifyAccepted:
		embed.Title = "✅ Follow Request Accepted"
		embed.Description = "A follow request has been approved by admin."
		embed.Color = ColorBlue
	case NotifyRejected:
		embed.Title = "🚫 Follow Request Rejected"
		embed.Description = "A follow request has been rejected by admin."
		embed.Color = ColorGray
	case NotifyBlocked:
		embed.Title = "🛡️ Blocked Server Attempted Registration"
		embed.Description = "A blocked server attempted to register with the relay."
		embed.Color = ColorOrange
	}

	payload := WebhookPayload{
		Username:  serviceName,
		AvatarURL: serviceIconURL,
		Embeds:    []Embed{embed},
	}

	go sendWebhook(payload)
}

func sendWebhook(payload WebhookPayload) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		logrus.Error("Failed to marshal Discord webhook payload: ", err)
		return
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		logrus.Error("Failed to send Discord webhook: ", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logrus.Error("Discord webhook returned non-2xx status: ", resp.StatusCode)
	}
}
