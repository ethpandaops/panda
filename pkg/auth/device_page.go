package auth

import (
	"fmt"
	"html"
)

// buildDevicePage generates the HTML page for entering a device authorization code.
func buildDevicePage(userCode, errorMsg string) string { //nolint:funlen // single HTML template
	errorHTML := ""
	if errorMsg != "" {
		errorHTML = fmt.Sprintf(`<div class="error-banner">%s</div>`, html.EscapeString(errorMsg))
	}

	prefill := ""
	if userCode != "" {
		prefill = html.EscapeString(userCode)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Device Authorization — panda</title>
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
    --accent: #58a6ff;
    --error: #f85149;
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
    max-width: 480px;
    position: relative;
    animation: fadeUp 0.5s ease-out;
  }

  @keyframes fadeUp {
    from { opacity: 0; transform: translateY(12px); }
    to   { opacity: 1; transform: translateY(0); }
  }

  .heading {
    font-size: 20px;
    font-weight: 600;
    color: var(--text-bright);
    margin-bottom: 8px;
    letter-spacing: -0.01em;
  }

  .subtext {
    font-size: 13px;
    color: var(--text-muted);
    margin-bottom: 32px;
    line-height: 1.5;
  }

  .error-banner {
    background: rgba(248, 81, 73, 0.1);
    border: 1px solid rgba(248, 81, 73, 0.4);
    border-radius: 8px;
    padding: 12px 16px;
    margin-bottom: 20px;
    font-size: 13px;
    color: var(--error);
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .code-input {
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 16px 20px;
    font-family: var(--mono);
    font-size: 24px;
    font-weight: 600;
    color: var(--text-bright);
    text-align: center;
    letter-spacing: 0.15em;
    text-transform: uppercase;
    outline: none;
    transition: border-color 0.2s;
  }

  .code-input:focus {
    border-color: var(--accent);
  }

  .code-input::placeholder {
    color: var(--text-muted);
    font-weight: 400;
    letter-spacing: 0.05em;
    text-transform: none;
  }

  .submit-btn {
    background: var(--accent);
    color: #fff;
    border: none;
    border-radius: 8px;
    padding: 14px 20px;
    font-family: var(--mono);
    font-size: 14px;
    font-weight: 600;
    cursor: pointer;
    transition: opacity 0.2s;
  }

  .submit-btn:hover {
    opacity: 0.9;
  }

  .footer {
    margin-top: 32px;
    padding-top: 20px;
    border-top: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-muted);
    line-height: 1.6;
  }
</style>
</head>
<body>
  <div class="container">
    <div class="heading">Device Authorization</div>
    <div class="subtext">Enter the code shown in your terminal to authorize this device.</div>
    %[1]s
    <form method="POST" action="/auth/device/verify">
      <input type="text" name="user_code" class="code-input"
             placeholder="XXXX-XXXX" maxlength="9" autocomplete="off"
             autofocus value="%[2]s">
      <button type="submit" class="submit-btn">Authorize</button>
    </form>
    <div class="footer">
      This page is part of the panda device authorization flow.<br>
      If you didn't initiate this, you can close this window.
    </div>
  </div>
</body>
</html>`, errorHTML, prefill)
}

// buildDeviceSuccessPage generates the HTML page shown after successful device authorization.
func buildDeviceSuccessPage(login string) string { //nolint:funlen // single HTML template
	displayLogin := html.EscapeString(login)
	if displayLogin == "" {
		displayLogin = "user"
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Device Authorized — panda</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<style>
  :root {
    --bg: #060a12;
    --surface: #0c1219;
    --border: #1b2535;
    --border-subtle: #141c28;
    --text: #c9d1d9;
    --text-bright: #ecf2f8;
    --text-muted: #545d68;
    --success: #3fb950;
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
    max-width: 480px;
    position: relative;
    animation: fadeUp 0.5s ease-out;
    text-align: center;
  }

  @keyframes fadeUp {
    from { opacity: 0; transform: translateY(12px); }
    to   { opacity: 1; transform: translateY(0); }
  }

  .status-icon {
    width: 48px;
    height: 48px;
    background: var(--success);
    border-radius: 50%%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    margin-bottom: 20px;
    box-shadow: 0 0 24px rgba(63, 185, 80, 0.3);
    animation: pulseIn 0.6s ease-out;
  }

  .status-icon svg {
    width: 24px;
    height: 24px;
    stroke: #fff;
    stroke-width: 3;
    fill: none;
  }

  @keyframes pulseIn {
    0%%   { transform: scale(0); opacity: 0; }
    60%%  { transform: scale(1.15); }
    100%% { transform: scale(1); opacity: 1; }
  }

  .heading {
    font-size: 20px;
    font-weight: 600;
    color: var(--text-bright);
    margin-bottom: 8px;
  }

  .subtext {
    font-size: 13px;
    color: var(--text-muted);
    margin-bottom: 32px;
    line-height: 1.5;
  }

  .close-notice {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 14px 18px;
    font-size: 14px;
    font-weight: 500;
    color: var(--text-bright);
  }
</style>
</head>
<body>
  <div class="container">
    <div class="status-icon">
      <svg viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12"/></svg>
    </div>
    <div class="heading">Device authorized</div>
    <div class="subtext">%s, your device has been authorized successfully.</div>
    <div class="close-notice">You can close this window and return to your terminal.</div>
  </div>
</body>
</html>`, displayLogin)
}
