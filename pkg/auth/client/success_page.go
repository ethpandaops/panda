package client

import (
	"encoding/base64"
	"fmt"
	"html"
	"strings"
)

// callbackUser holds user info extracted from the OAuth callback redirect.
type callbackUser struct {
	Login     string
	AvatarURL string
	Orgs      []string
}

// specialUsers get ASCII art instead of the GIF.
var specialUsers = map[string]bool{
	"samcm":     true,
	"mattevans": true,
	"savid":     true,
}

// pandaASCIIBase64 is the base64-encoded ASCII art shown to special users.
// To update: write your raw multiline ASCII art to a file, then:
//
//	base64 < art.txt | tr -d '\n'
//
// Paste the output here.
var pandaASCIIBase64 = "KiBnIG8gYSB0IHMgZSB4ICogZyBvIGEgdCBzIGUgeCAqIGcgbyBhIHQgcyBlIHggKgpnICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICBnICAKbyAvICAgICBcICAgICAgICAgICAgIFwgICAgICAgICAgICAvICAgIFwgICAgICAgbwphfCAgICAgICB8ICAgICAgICAgICAgIFwgICAgICAgICAgfCAgICAgIHwgICAgICBhCnR8ICAgICAgIGAuICAgICAgICAgICAgIHwgICAgICAgICB8ICAgICAgIDogICAgIHQKc2AgICAgICAgIHwgICAgICAgICAgICAgfCAgICAgICAgXHwgICAgICAgfCAgICAgcwplIFwgICAgICAgfCAvICAgICAgIC8gIFxcXCAgIC0tX18gXFwgICAgICAgOiAgICBlCnggIFwgICAgICBcLyAgIF8tLX5+ICAgICAgICAgIH4tLV9ffCBcICAgICB8ICAgIHggIAoqICAgXCAgICAgIFxfLX4gICAgICAgICAgICAgICAgICAgIH4tX1wgICAgfCAgICAqCmcgICAgXF8gICAgIFwgICAgICAgIF8uLS0tLS0tLS0uX19fX19fXHwgICB8ICAgIGcKbyAgICAgIFwgICAgIFxfX19fX18vLyBfIF9fXyBfIChfKF9fPiAgXCAgIHwgICAgbwphICAgICAgIFwgICAuICBDIF9fXykgIF9fX19fXyAoXyhfX19fPiAgfCAgLyAgICBhCnQgICAgICAgL1wgfCAgIEMgX19fXykvICAgICAgXCAoX19fX18+ICB8Xy8gICAgIHQKcyAgICAgIC8gL1x8ICAgQ19fX19fKSAgICAgICB8ICAoX19fPiAgIC8gIFwgICAgcwplICAgICB8ICAgKCAgIF9DX19fX18pXF9fX19fXy8gIC8vIF8vIC8gICAgIFwgICBlCnggICAgIHwgICAgXCAgfF9fICAgXFxfX19fX19fX18vLyAoX18vICAgICAgIHwgIHgKKiAgICB8IFwgICAgXF9fX18pICAgYC0tLS0gICAtLScgICAgICAgICAgICAgfCAgKgpnICAgIHwgIFxfICAgICAgICAgIF9fX1wgICAgICAgL18gICAgICAgICAgXy8gfCBnCm8gICB8ICAgICAgICAgICAgICAvICAgIHwgICAgIHwgIFwgICAgICAgICAgICB8IG8KYSAgIHwgICAgICAgICAgICAgfCAgICAvICAgICAgIFwgIFwgICAgICAgICAgIHwgYQp0ICAgfCAgICAgICAgICAvIC8gICAgfCAgICAgICAgIHwgIFwgICAgICAgICAgIHx0CnMgICB8ICAgICAgICAgLyAvICAgICAgXF9fL1xfX18vICAgIHwgICAgICAgICAgfHMKZSAgfCAgICAgICAgICAgLyAgICAgICAgfCAgICB8ICAgICAgIHwgICAgICAgICB8ZQp4ICB8ICAgICAgICAgIHwgICAgICAgICB8ICAgIHwgICAgICAgfCAgICAgICAgIHx4CiogZyBvIGEgdCBzIGUgeCAqIGcgbyBhIHQgcyBlIHggKiBnIG8gYSB0IHMgZSB4ICoK" //nolint:lll // base64 blob

// decodePandaASCII decodes the base64-encoded ASCII art.
func decodePandaASCII() string {
	b, err := base64.StdEncoding.DecodeString(pandaASCIIBase64)
	if err != nil {
		return "(panda art failed to decode)"
	}

	return string(b)
}

// buildSuccessPage generates a styled HTML success page based on user info.
func buildSuccessPage(user callbackUser) string { //nolint:funlen // single HTML template
	isEthPandaOps := hasOrg(user.Orgs, "ethpandaops")
	isSpecialUser := specialUsers[strings.ToLower(user.Login)]

	login := html.EscapeString(user.Login)
	if login == "" {
		login = "user"
	}

	avatarHTML := ""
	if user.AvatarURL != "" {
		avatarHTML = fmt.Sprintf(
			`<img src="%s" alt="" class="avatar">`,
			html.EscapeString(user.AvatarURL),
		)
	} else {
		avatarHTML = `<div class="avatar avatar-fallback">` + strings.ToUpper(login[:1]) + `</div>`
	}

	// Build the context line — what org, what just happened.
	contextHTML := `<span class="context-muted">GitHub identity linked</span>`
	if isEthPandaOps {
		contextHTML = `<span class="org-badge">ethpandaops</span>`
	}

	// Status subtitle.
	statusSub := "You've successfully logged in to panda"
	if isEthPandaOps {
		statusSub = "You've successfully logged in to ethpandaops/panda"
	}

	// Tagline — shown after the media block.
	tagline := "You can close this window and return to your terminal."
	if isEthPandaOps {
		tagline = "Enjoy debugging your devnet champ"
	}

	// Media — GIF or ASCII art for ethpandaops members.
	mediaHTML := ""
	if isEthPandaOps {
		if isSpecialUser {
			art := decodePandaASCII()
			mediaHTML = fmt.Sprintf(`<pre class="ascii-art">%s</pre>`, html.EscapeString(art))
		} else {
			mediaHTML = `<div class="media-frame"><img src="https://media1.tenor.com/m/92A2K1kvoHcAAAAd/casino-royale-bond.gif" alt="" class="gif"></div>` //nolint:lll // gif URL
		}
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authenticated — panda</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<style>
  :root {
    --bg: #060a12;
    --surface: #0c1219;
    --surface-raised: #111921;
    --border: #1b2535;
    --border-subtle: #141c28;
    --text: #c9d1d9;
    --text-bright: #ecf2f8;
    --text-muted: #545d68;
    --success: #3fb950;
    --success-dim: #238636;
    --accent: #58a6ff;
    --code-bg: #0a0f18;
    --mono: "IBM Plex Mono", "SF Mono", "Fira Code", ui-monospace, monospace;
  }

  *, *::before, *::after { margin: 0; padding: 0; box-sizing: border-box; }

  body {
    background: var(--bg);
    color: var(--text);
    font-family: var(--mono);
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    -webkit-font-smoothing: antialiased;
  }

  /* Subtle grid background */
  body::before {
    content: "";
    position: fixed;
    inset: 0;
    background-image:
      linear-gradient(var(--border-subtle) 1px, transparent 1px),
      linear-gradient(90deg, var(--border-subtle) 1px, transparent 1px);
    background-size: 60px 60px;
    opacity: 0.3;
    pointer-events: none;
  }

  .container {
    width: 100%%;
    max-width: 600px;
    position: relative;
    animation: fadeUp 0.5s ease-out;
  }

  @keyframes fadeUp {
    from { opacity: 0; transform: translateY(12px); }
    to   { opacity: 1; transform: translateY(0); }
  }

  /* ── Status indicator ── */
  .status {
    margin-bottom: 32px;
    padding-bottom: 24px;
    border-bottom: 1px solid var(--border);
  }

  .status-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 6px;
  }

  .status-icon {
    width: 32px;
    height: 32px;
    background: var(--success);
    border-radius: 50%%;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    box-shadow: 0 0 20px rgba(63, 185, 80, 0.3);
    animation: pulseIn 0.6s ease-out;
  }

  .status-icon svg {
    width: 18px;
    height: 18px;
    stroke: #fff;
    stroke-width: 3;
    fill: none;
  }

  @keyframes pulseIn {
    0%%   { transform: scale(0); opacity: 0; }
    60%%  { transform: scale(1.15); }
    100%% { transform: scale(1); opacity: 1; }
  }

  .status-heading {
    font-size: 20px;
    font-weight: 600;
    color: var(--text-bright);
    letter-spacing: -0.01em;
  }

  .status-sub {
    font-size: 12px;
    color: var(--text-muted);
    padding-left: 44px;
  }

  /* ── Identity block ── */
  .identity {
    display: flex;
    align-items: center;
    gap: 14px;
    margin-bottom: 24px;
  }

  .avatar {
    width: 44px;
    height: 44px;
    border-radius: 10px;
    flex-shrink: 0;
  }

  .avatar-fallback {
    background: var(--surface-raised);
    border: 1px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 16px;
    font-weight: 600;
    color: var(--text-muted);
  }

  .identity-info {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }

  .username {
    font-size: 16px;
    font-weight: 600;
    color: var(--text-bright);
    line-height: 1.2;
  }

  .org-badge {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11px;
    font-weight: 500;
    color: var(--accent);
    letter-spacing: 0.02em;
  }

  .org-badge::before {
    content: "";
    width: 5px;
    height: 5px;
    background: var(--accent);
    border-radius: 50%%;
    opacity: 0.6;
  }

  .context-muted {
    font-size: 11px;
    color: var(--text-muted);
    letter-spacing: 0.02em;
  }

  /* ── Message ── */
  .message {
    font-size: 14px;
    line-height: 1.6;
    color: var(--text);
    margin-bottom: 24px;
  }

  /* ── Media ── */
  .media-frame {
    margin-bottom: 24px;
    border-radius: 8px;
    overflow: hidden;
    border: 1px solid var(--border);
  }

  .gif {
    display: block;
    width: 100%%;
  }

  .ascii-art {
    font-family: var(--mono);
    font-size: 10px;
    line-height: 1.35;
    color: var(--accent);
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 20px;
    margin-bottom: 24px;
    overflow-x: auto;
    white-space: pre;
  }

  /* ── Next steps ── */
  .next-steps {
    border-top: 1px solid var(--border);
    padding-top: 20px;
  }

  .next-label {
    font-size: 10px;
    font-weight: 600;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--text-muted);
    margin-bottom: 14px;
  }

  .step {
    font-size: 12px;
    color: var(--text);
    line-height: 1.5;
    margin-bottom: 14px;
  }

  .step:last-child {
    margin-bottom: 0;
  }

  .cmd {
    display: block;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 10px 14px;
    margin-top: 6px;
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text-muted);
    overflow-x: auto;
  }

  .cmd .prompt {
    color: var(--success);
    user-select: none;
  }

  .cmd .arg {
    color: var(--text);
  }

</style>
</head>
<body>
  <div class="container">
    <div class="status">
      <div class="status-row">
        <div class="status-icon"><svg viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12"/></svg></div>
        <span class="status-heading">Authentication successful</span>
      </div>
      <div class="status-sub">%[6]s</div>
    </div>

    <div class="identity">
      %[1]s
      <div class="identity-info">
        <span class="username">%[2]s</span>
        %[3]s
      </div>
    </div>

    %[5]s

    <div class="message">%[4]s</div>

    <div class="next-steps">
      <div class="next-label">Next steps</div>
      <div class="step">
        Return to your terminal. panda is ready.
        <code class="cmd"><span class="prompt">$</span> <span class="arg">panda datasources</span></code>
      </div>
      <div class="step">
        Run queries against your connected datasources.
        <code class="cmd"><span class="prompt">$</span> <span class="arg">panda execute --code 'print("hello")'</span></code>
      </div>
    </div>

  </div>
</body>
</html>`,
		avatarHTML,
		login,
		contextHTML,
		html.EscapeString(tagline),
		mediaHTML,
		html.EscapeString(statusSub),
	)
}

// hasOrg checks if the user belongs to the given org (case-insensitive).
func hasOrg(orgs []string, target string) bool {
	lower := strings.ToLower(target)
	for _, org := range orgs {
		if strings.ToLower(org) == lower {
			return true
		}
	}

	return false
}
