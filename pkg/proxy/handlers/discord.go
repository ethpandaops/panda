package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	discordAPIBase = "https://discord.com/api/v10"
)

// DiscordConfig holds Discord configuration for sending build completion DMs.
type DiscordConfig struct {
	BotToken string
	GuildID  string
}

// DiscordClient sends Discord DMs as a bot user. Resolution accepts either a
// numeric user ID (preferred, unambiguous) or a username string that will be
// looked up against the configured guild.
type DiscordClient struct {
	log        logrus.FieldLogger
	cfg        DiscordConfig
	httpClient *http.Client
}

// NewDiscordClient returns a client configured to act as the bot. Returns nil
// if no bot token is configured.
func NewDiscordClient(log logrus.FieldLogger, cfg DiscordConfig) *DiscordClient {
	if cfg.BotToken == "" {
		return nil
	}

	return &DiscordClient{
		log:        log.WithField("handler", "discord"),
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// SendDM resolves the target and posts a DM. Errors are returned for the
// caller to log — they are not surfaced to the user who triggered the build.
func (c *DiscordClient) SendDM(ctx context.Context, target, message string) error {
	target = strings.TrimSpace(strings.TrimPrefix(target, "@"))
	if target == "" {
		return fmt.Errorf("empty target")
	}

	userID, err := c.resolveUserID(ctx, target)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", target, err)
	}

	channelID, err := c.openDMChannel(ctx, userID)
	if err != nil {
		return fmt.Errorf("opening DM channel: %w", err)
	}

	return c.postMessage(ctx, channelID, message)
}

// resolveUserID accepts either a numeric snowflake or a username. Usernames are
// resolved by searching the configured guild(s).
func (c *DiscordClient) resolveUserID(ctx context.Context, target string) (string, error) {
	if isSnowflake(target) {
		return target, nil
	}

	if c.cfg.GuildID == "" {
		return "", fmt.Errorf("guild_id not configured; pass a Discord user ID instead of a username")
	}

	for _, guildID := range strings.Split(c.cfg.GuildID, ",") {
		guildID = strings.TrimSpace(guildID)
		if guildID == "" {
			continue
		}

		id, err := c.searchGuildMember(ctx, guildID, target)
		if err != nil {
			c.log.WithError(err).WithField("guild_id", guildID).
				Debug("Guild member search failed")

			continue
		}

		if id != "" {
			return id, nil
		}
	}

	return "", fmt.Errorf("no member matching %q found in configured guilds", target)
}

func (c *DiscordClient) searchGuildMember(ctx context.Context, guildID, query string) (string, error) {
	endpoint := fmt.Sprintf("%s/guilds/%s/members/search?query=%s&limit=10",
		discordAPIBase, guildID, urlQueryEscape(query))

	var members []struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}

	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &members); err != nil {
		return "", err
	}

	for _, m := range members {
		if strings.EqualFold(m.User.Username, query) {
			return m.User.ID, nil
		}
	}

	if len(members) > 0 {
		return members[0].User.ID, nil
	}

	return "", nil
}

func (c *DiscordClient) openDMChannel(ctx context.Context, userID string) (string, error) {
	body := map[string]string{"recipient_id": userID}

	var resp struct {
		ID string `json:"id"`
	}

	if err := c.doJSON(ctx, http.MethodPost, discordAPIBase+"/users/@me/channels", body, &resp); err != nil {
		return "", err
	}

	if resp.ID == "" {
		return "", fmt.Errorf("discord returned empty channel id")
	}

	return resp.ID, nil
}

func (c *DiscordClient) postMessage(ctx context.Context, channelID, content string) error {
	body := map[string]string{"content": content}
	endpoint := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, channelID)

	return c.doJSON(ctx, http.MethodPost, endpoint, body, nil)
}

func (c *DiscordClient) doJSON(ctx context.Context, method, url string, body, out any) error {
	var reader io.Reader

	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling body: %w", err)
		}

		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bot "+c.cfg.BotToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("discord HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if out == nil {
		return nil
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// isSnowflake reports whether s is a numeric Discord snowflake.
func isSnowflake(s string) bool {
	if len(s) < 15 || len(s) > 20 {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// urlQueryEscape escapes a query value without importing net/url just for this.
func urlQueryEscape(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}

	return b.String()
}
