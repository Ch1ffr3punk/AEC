package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha3"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "image/png"
	
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20"
	"golang.org/x/net/proxy"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type purpleTheme struct {
	base fyne.Theme
}

func (p *purpleTheme) Font(s fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(s)
}

func (p *purpleTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 122, G: 110, B: 243, A: 255}
	case theme.ColorNameForegroundOnPrimary:
		return color.White
	case theme.ColorNameHyperlink:
		return color.NRGBA{R: 122, G: 110, B: 243, A: 255}
	default:
		return p.base.Color(name, variant)
	}
}

func (p *purpleTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return p.base.Icon(name)
}

func (p *purpleTheme) Size(name fyne.ThemeSizeName) float32 {
	return p.base.Size(name)
}

type esub struct {
	key     string
	subject string
}

type NNTPConfig struct {
	Server      string `json:"server"`
	Port        int    `json:"port"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Newsgroup   string `json:"newsgroup"`
	UseProxy    bool   `json:"use_proxy"`
	ProxyPort   int    `json:"proxy_port"`
	LastArticle int    `json:"last_article"`
}

type EsubApp struct {
	app               fyne.App
	window            fyne.Window
	statusLabel       *widget.Label
	progressBar       *widget.ProgressBar
	progressLabel     *widget.Label
	progressContainer *fyne.Container
	db                *sql.DB
	replayCache       map[string]bool
	dbMutex           sync.RWMutex
	configPath        string
	dbPath            string
	isDarkTheme       bool
	themeSwitch       *widget.Button
	articlesDir       string
}

var (
	rcFlag         = flag.Bool("rc", true, "enable replay cache")
	ErrNoNewArticles = errors.New("no new articles to fetch")
)

func getDataDir() string {
	if runtime.GOOS == "android" {
		downloadDir := filepath.Join(os.Getenv("HOME"), "Downloads")
		if _, err := os.Stat(downloadDir); err == nil {
			return filepath.Join(downloadDir, "Fetch")
		}
		externalStorage := "/sdcard/Download"
		if _, err := os.Stat(externalStorage); err == nil {
			return filepath.Join(externalStorage, "Fetch")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func getArticlesDir() string {
	return filepath.Join(getDataDir(), "Articles")
}

func getConfigPath() string {
	return filepath.Join(getDataDir(), "nntp_config.json")
}

func getDBPath() string {
	return filepath.Join(getDataDir(), "esub_rc.db")
}

func (e *esub) deriveKey() []byte {
	pepper := []byte("fixed-pepper-1234")
	return argon2.IDKey(
		[]byte(e.key),
		pepper,
		3,
		64*1024,
		4,
		32,
	)
}

func (e *esub) esubgen() string {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}

	key := e.deriveKey()
	cipher, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		panic(err)
	}

	textHash := sha3.Sum256([]byte("text"))
	ciphertext := make([]byte, 12)
	cipher.XORKeyStream(ciphertext, textHash[:12])

	return hex.EncodeToString(append(nonce, ciphertext...))
}

func (e *esub) esubtest() bool {
	if len(e.subject) != 48 {
		return false
	}

	esubBytes, err := hex.DecodeString(e.subject)
	if err != nil || len(esubBytes) != 24 {
		return false
	}

	nonce := esubBytes[:12]
	receivedCiphertext := esubBytes[12:]

	key := e.deriveKey()
	cipher, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		return false
	}

	textHash := sha3.Sum256([]byte("text"))
	expectedCiphertext := make([]byte, 12)
	cipher.XORKeyStream(expectedCiphertext, textHash[:12])

	return hex.EncodeToString(expectedCiphertext) == hex.EncodeToString(receivedCiphertext)
}

func (app *EsubApp) initDB() error {
	if !*rcFlag {
		return nil
	}

	if dir := filepath.Dir(app.dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	var err error
	app.db, err = sql.Open("sqlite3", app.dbPath)
	if err != nil {
		return err
	}

	_, err = app.db.Exec(`CREATE TABLE IF NOT EXISTS esubs (
		esub_hex TEXT PRIMARY KEY,
		first_seen TEXT NOT NULL,
		article_id INTEGER,
		newsgroup TEXT
	)`)
	if err != nil {
		return err
	}

	rows, err := app.db.Query("SELECT esub_hex FROM esubs")
	if err != nil {
		return err
	}
	defer rows.Close()

	app.replayCache = make(map[string]bool)
	for rows.Next() {
		var esubHex string
		rows.Scan(&esubHex)
		app.replayCache[esubHex] = true
	}

	return nil
}

func (e *esub) checkReplayCache(app *EsubApp) bool {
	if !*rcFlag || app.db == nil {
		return false
	}

	app.dbMutex.RLock()
	defer app.dbMutex.RUnlock()

	if _, exists := app.replayCache[e.subject]; exists {
		return true
	}

	var count int
	app.db.QueryRow("SELECT COUNT(*) FROM esubs WHERE esub_hex = ?", e.subject).Scan(&count)
	return count > 0
}

func (e *esub) addToReplayCache(app *EsubApp, articleID int, newsgroup string) error {
	if !*rcFlag || app.db == nil {
		return nil
	}

	if e.checkReplayCache(app) {
		return nil
	}

	app.dbMutex.Lock()
	defer app.dbMutex.Unlock()

	_, err := app.db.Exec("INSERT INTO esubs (esub_hex, first_seen, article_id, newsgroup) VALUES (?, ?, ?, ?)",
		e.subject, time.Now().Format(time.RFC3339), articleID, newsgroup)
	if err != nil {
		return err
	}

	app.replayCache[e.subject] = true
	return nil
}

func (app *EsubApp) loadConfig() *NNTPConfig {
	file, err := os.Open(app.configPath)
	if err != nil {
		return &NNTPConfig{Server: "", Port: 119, Newsgroup: "", UseProxy: false, ProxyPort: 9050, LastArticle: 0}
	}
	defer file.Close()

	var config NNTPConfig
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return &NNTPConfig{Server: "", Port: 119, Newsgroup: "", UseProxy: false, ProxyPort: 9050, LastArticle: 0}
	}
	return &config
}

func (app *EsubApp) saveConfig(config *NNTPConfig) error {
	if dir := filepath.Dir(app.configPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	file, err := os.Create(app.configPath)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(config)
}

func (app *EsubApp) getThemeIcon() string {
	if app.isDarkTheme {
		return "☀️"
	}
	return "🌙"
}

func (app *EsubApp) toggleTheme() {
	if app.isDarkTheme {
		app.app.Settings().SetTheme(&purpleTheme{base: theme.LightTheme()})
		app.isDarkTheme = false
	} else {
		app.app.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})
		app.isDarkTheme = true
	}

	if app.themeSwitch != nil {
		app.themeSwitch.SetText(app.getThemeIcon())
		app.themeSwitch.Refresh()
	}
	app.window.Content().Refresh()
}

func (app *EsubApp) connectToNNTP(config *NNTPConfig) (net.Conn, *bufio.Reader, error) {
	addr := fmt.Sprintf("%s:%d", config.Server, config.Port)

	var conn net.Conn
	var err error

	if config.UseProxy {
		proxyAddr := fmt.Sprintf("127.0.0.1:%d", config.ProxyPort)
		dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
		if err != nil {
			return nil, nil, fmt.Errorf("SOCKS5 dialer error: %v", err)
		}

		conn, err = dialer.Dial("tcp", addr)
		if err != nil {
			return nil, nil, fmt.Errorf("proxy connection failed: %v", err)
		}
	} else {
		conn, err = net.DialTimeout("tcp", addr, 30*time.Second)
		if err != nil {
			return nil, nil, err
		}
	}

	reader := bufio.NewReader(conn)

	response, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	if !strings.HasPrefix(response, "200") && !strings.HasPrefix(response, "201") {
		conn.Close()
		return nil, nil, fmt.Errorf("NNTP server error: %s", response)
	}

	if config.Username != "" {
		fmt.Fprintf(conn, "AUTHINFO USER %s\r\n", config.Username)
		response, err = reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, err
		}

		if strings.HasPrefix(response, "381") {
			fmt.Fprintf(conn, "AUTHINFO PASS %s\r\n", config.Password)
			response, err = reader.ReadString('\n')
			if err != nil {
				conn.Close()
				return nil, nil, err
			}
			if !strings.HasPrefix(response, "281") {
				conn.Close()
				return nil, nil, fmt.Errorf("authentication failed")
			}
		}
	}

	return conn, reader, nil
}

func (app *EsubApp) fetchArticlesFromNewsgroup() {
	config := app.loadConfig()

	if config.Server == "" || config.Newsgroup == "" {
		dialog.ShowError(errors.New("Please configure NNTP server and Newsgroup"), app.window)
		return
	}

	keyEntry := widget.NewPasswordEntry()
	keyEntry.SetPlaceHolder("")

	items := []*widget.FormItem{
		widget.NewFormItem("Password", keyEntry),
	}

	dialog.ShowForm("", "Start", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}

		key := strings.TrimSpace(keyEntry.Text)
		if key == "" {
			dialog.ShowError(errors.New("Password cannot be empty"), app.window)
			return
		}

		app.progressContainer.Show()
		app.progressBar.SetValue(0)
		app.progressLabel.SetText("0%")
		app.statusLabel.SetText("Connecting to NNTP server...")

		go func() {
			currentConfig := app.loadConfig()
			err := app.processNewsgroup(currentConfig, key)
			fyne.Do(func() {
				if err != nil {
					if errors.Is(err, ErrNoNewArticles) {
						app.statusLabel.SetText("No new articles to fetch")
						app.progressContainer.Hide()
					} else {
						dialog.ShowError(err, app.window)
						app.statusLabel.SetText("Failed")
						app.progressContainer.Hide()
					}
				} else {
					app.statusLabel.SetText("Articles saved to " + app.articlesDir)
					app.progressBar.SetValue(1)
					app.progressLabel.SetText("100%")
					time.AfterFunc(3*time.Second, func() {
						fyne.Do(func() {
							app.progressContainer.Hide()
						})
					})
				}
			})
		}()
	}, app.window)
}

func (app *EsubApp) processNewsgroup(config *NNTPConfig, key string) error {
	conn, reader, err := app.connectToNNTP(config)
	if err != nil {
		return err
	}
	defer conn.Close()

	fmt.Fprintf(conn, "GROUP %s\r\n", config.Newsgroup)
	response, err := reader.ReadString('\n')
	if err != nil {
		return err
	}

	var articleCount, first, last int
	_, err = fmt.Sscanf(response, "211 %d %d %d", &articleCount, &first, &last)
	if err != nil {
		return fmt.Errorf("failed to parse GROUP response: %v", err)
	}

	if first == 0 || last == 0 {
		return fmt.Errorf("no articles in Newsgroup")
	}

	startArticle := first

	if config.LastArticle > 0 {
		if config.LastArticle >= last {
			fyne.Do(func() {
				app.statusLabel.SetText(fmt.Sprintf("No new articles (last processed: %d, server last: %d)",
					config.LastArticle, last))
			})
			return ErrNoNewArticles
		}
		if config.LastArticle >= first {
			startArticle = config.LastArticle + 1
			fyne.Do(func() {
				app.statusLabel.SetText(fmt.Sprintf("Resuming from article %d (last processed: %d)",
					startArticle, config.LastArticle))
			})
			time.Sleep(2 * time.Second)
		}
	}

	totalArticles := last - startArticle + 1
	if totalArticles <= 0 {
		return ErrNoNewArticles
	}

	current := 0

	fyne.Do(func() {
		if startArticle > first {
			app.statusLabel.SetText(fmt.Sprintf("Fetching new articles %d to %d...", startArticle, last))
		} else {
			app.statusLabel.SetText(fmt.Sprintf("Fetching articles %d to %d...", first, last))
		}
		app.progressBar.SetValue(0)
		app.progressLabel.SetText("0%")
	})

	if err := os.MkdirAll(app.articlesDir, 0755); err != nil {
		return err
	}

	foundCount := 0
	maxProcessed := startArticle - 1

	for msgID := startArticle; msgID <= last; msgID++ {
		current++
		percent := float64(current) / float64(totalArticles)

		if msgID > maxProcessed {
			maxProcessed = msgID
		}

		fyne.Do(func() {
			app.progressBar.SetValue(percent)
			app.progressLabel.SetText(fmt.Sprintf("%d%%", int(percent*100)))
			app.statusLabel.SetText(fmt.Sprintf("Article %d/%d (new: %d, total found: %d)", msgID, last, current, foundCount))
		})

		fmt.Fprintf(conn, "ARTICLE %d\r\n", msgID)
		response, err := reader.ReadString('\n')
		if err != nil {
			continue
		}

		if !strings.HasPrefix(response, "220") {
			continue
		}

		var article strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			if line == ".\r\n" {
				break
			}
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			article.WriteString(line)
		}

		articleStr := article.String()
		var subject string

		lines := strings.Split(articleStr, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "Subject:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					subject = strings.TrimSpace(parts[1])
					break
				}
			}
			if strings.HasPrefix(line, "X-Esub:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					subject = strings.TrimSpace(parts[1])
					break
				}
			}
		}

		if subject != "" && len(subject) == 48 {
			e := &esub{key: key, subject: subject}
			if e.esubtest() {
				if e.checkReplayCache(app) {
					continue
				}

				outputFileName := filepath.Join(app.articlesDir, fmt.Sprintf("article_%d_%s.txt", msgID, e.subject))
				outputFile, err := os.Create(outputFileName)
				if err != nil {
					return err
				}

				outputFile.WriteString(fmt.Sprintf("Article-ID: %d\n", msgID))
				outputFile.WriteString(fmt.Sprintf("esub: %s\n", e.subject))
				outputFile.WriteString("---\n")
				outputFile.WriteString(articleStr)
				outputFile.Close()

				e.addToReplayCache(app, msgID, config.Newsgroup)
				foundCount++

				fyne.Do(func() {
					app.statusLabel.SetText(fmt.Sprintf("Found new esub #%d (article %d)", foundCount, msgID))
				})
			}
		}

		time.Sleep(100 * time.Millisecond)

		if msgID%100 == 0 {
			config.LastArticle = maxProcessed
			app.saveConfig(config)
		}
	}

	if maxProcessed > startArticle-1 {
		config.LastArticle = maxProcessed
		app.saveConfig(config)
	}

	if foundCount == 0 && startArticle == first {
		return fmt.Errorf("no valid esub(s) found in Newsgroup %s", config.Newsgroup)
	} else if foundCount == 0 && startArticle > first {
		return fmt.Errorf("no new valid esub(s) found in Newsgroup %s (last processed: %d)", config.Newsgroup, startArticle-1)
	}

	return nil
}

func (app *EsubApp) displayArticle() {
	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, app.window)
			return
		}
		if reader == nil {
			return
		}
		defer reader.Close()

		content, err := io.ReadAll(reader)
		if err != nil {
			dialog.ShowError(fmt.Errorf("error reading file: %v", err), app.window)
			return
		}

		articleStr := string(content)
		separator := "---\n"
		sepIdx := strings.Index(articleStr, separator)
		if sepIdx != -1 {
			articleStr = articleStr[sepIdx+len(separator):]
		}

		images, err := decodeMultipartImages(articleStr)
		if err != nil {
			dialog.ShowError(fmt.Errorf("error decoding: %v", err), app.window)
			return
		}

		if len(images) == 0 {
			dialog.ShowInformation("No Images", "No AEC images found in article.", app.window)
			return
		}

		app.showImageDialog(images[0], len(images))
	}, app.window)
}

func decodeMultipartImages(article string) ([][]byte, error) {
	var images [][]byte

	boundary := ""
	lines := strings.Split(article, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Content-Type: multipart/") {
			if idx := strings.Index(line, "boundary="); idx != -1 {
				boundaryPart := line[idx+9:]
				boundary = strings.Trim(boundaryPart, "\";")
				break
			}
		}
	}

	if boundary == "" {
		for _, line := range lines {
			if strings.HasPrefix(line, "----=_Part") {
				boundary = strings.TrimSpace(line)
				break
			}
		}
	}

	if boundary == "" {
		return nil, errors.New("multipart boundary not found")
	}

	parts := strings.Split(article, boundary)
	for _, part := range parts {
		if strings.Contains(part, "Content-Type: image/png") {
			imageData, err := extractPNGFromPart(part)
			if err == nil && len(imageData) > 0 {
				images = append(images, imageData)
			}
		}
	}

	return images, nil
}

func extractPNGFromPart(part string) ([]byte, error) {
	lines := strings.Split(part, "\n")
	var base64Lines []string
	inData := false

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "Content-Transfer-Encoding: base64") {
			inData = true
			continue
		}
		if inData && line == "" {
			continue
		}
		if inData && !strings.HasPrefix(line, "Content-") && line != "" && !strings.HasPrefix(line, "--") {
			base64Lines = append(base64Lines, line)
		}
		if inData && strings.HasPrefix(line, "Content-Type:") {
			break
		}
	}

	if len(base64Lines) == 0 {
		return nil, errors.New("no base64 data found")
	}

	base64Str := strings.Join(base64Lines, "")
	return base64.StdEncoding.DecodeString(base64Str)
}

func (app *EsubApp) showImageDialog(imageData []byte, totalImages int) {
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		dialog.ShowError(fmt.Errorf("error decoding PNG: %v", err), app.window)
		return
	}

	bounds := img.Bounds()
	
	scaledImg := image.NewRGBA(image.Rect(0, 0, 512, 512))
	
	for y := 0; y < 512; y++ {
		srcY := y * bounds.Dy() / 512
		for x := 0; x < 512; x++ {
			srcX := x * bounds.Dx() / 512
			scaledImg.Set(x, y, img.At(srcX, srcY))
		}
	}
	
	fyneImg := canvas.NewImageFromImage(scaledImg)
	fyneImg.FillMode = canvas.ImageFillContain
	fyneImg.SetMinSize(fyne.NewSize(512, 512))
	
	infoText := "AEC Image"
	if totalImages > 1 {
		infoText = fmt.Sprintf("AEC Image 1 of %d", totalImages)
	}
	
	imageWindow := app.app.NewWindow("Article Image")
	
	closeBtn := widget.NewButton("Close", func() {
		imageWindow.Close()
	})
	closeBtn.Importance = widget.HighImportance
	
	content := container.NewVBox(
		widget.NewLabel(infoText),
		fyneImg,
		container.NewCenter(closeBtn),
	)
	
	imageWindow.Resize(fyne.NewSize(600, 700))
	imageWindow.SetContent(content)
	imageWindow.CenterOnScreen()
	imageWindow.Show()
}

func (app *EsubApp) showSettings() {
	config := app.loadConfig()

	serverEntry := widget.NewEntry()
	serverEntry.SetText(config.Server)
	portEntry := widget.NewEntry()
	portEntry.SetText(fmt.Sprintf("%d", config.Port))
	userEntry := widget.NewEntry()
	userEntry.SetText(config.Username)

	passEntry := widget.NewPasswordEntry()
	passEntry.SetText(config.Password)

	newsgroupEntry := widget.NewEntry()
	newsgroupEntry.SetText(config.Newsgroup)

	useProxyCheck := widget.NewCheck("Use Proxy", func(bool) {})
	useProxyCheck.SetChecked(config.UseProxy)

	proxyPortSelect := widget.NewSelect([]string{"1080 (Nym)", "9050 (Tor)", "9150 (Tor Browser)"}, func(selected string) {})
	defaultPort := fmt.Sprintf("%d", config.ProxyPort)
	if config.ProxyPort == 1080 {
		defaultPort = "1080 (Nym)"
	} else if config.ProxyPort == 9150 {
		defaultPort = "9150 (Tor Browser)"
	}
	proxyPortSelect.SetSelected(defaultPort)

	resetCacheBtn := widget.NewButton("Reset Cache & Start Over", func() {
		dialog.ShowConfirm("Reset Cache", "Delete all cached esubs and start from first article?", func(confirmed bool) {
			if confirmed && app.db != nil {
				app.db.Exec("DELETE FROM esubs")
				app.db.Exec("VACUUM")
				app.replayCache = make(map[string]bool)
				config.LastArticle = 0
				app.saveConfig(config)
				dialog.ShowInformation("", "Cache cleared! Next fetch will start from article 1.", app.window)
			}
		}, app.window)
	})

	items := []*widget.FormItem{
		widget.NewFormItem("NNTP Server", serverEntry),
		widget.NewFormItem("Port", portEntry),
		widget.NewFormItem("Username (optional)", userEntry),
		widget.NewFormItem("Password (optional)", passEntry),
		widget.NewFormItem("Newsgroup", newsgroupEntry),
		widget.NewFormItem("", useProxyCheck),
		widget.NewFormItem("Proxy Port", proxyPortSelect),
		widget.NewFormItem("", resetCacheBtn),
	}

	dialog.ShowForm("", "Save", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}

		var port int
		fmt.Sscanf(portEntry.Text, "%d", &port)
		if port == 0 {
			port = 119
		}

		var proxyPort int
		selected := proxyPortSelect.Selected
		if strings.Contains(selected, "1080") {
			proxyPort = 1080
		} else if strings.Contains(selected, "9050") {
			proxyPort = 9050
		} else if strings.Contains(selected, "9150") {
			proxyPort = 9150
		} else {
			proxyPort = 1080
		}

		newConfig := &NNTPConfig{
			Server:      serverEntry.Text,
			Port:        port,
			Username:    userEntry.Text,
			Password:    passEntry.Text,
			Newsgroup:   newsgroupEntry.Text,
			UseProxy:    useProxyCheck.Checked,
			ProxyPort:   proxyPort,
			LastArticle: config.LastArticle,
		}
		app.saveConfig(newConfig)
		dialog.ShowInformation("", "Configuration saved!", app.window)
	}, app.window)
}

func (app *EsubApp) showInfoPopup() {
	projURL, _ := url.Parse("https://github.com/Ch1ffr3punk/AEC")

	projectLink := widget.NewHyperlink("An Open Source project", projURL)

	okButton := widget.NewButton("OK", func() {
		overlays := app.window.Canvas().Overlays()
		if overlays.Top() != nil {
			overlays.Remove(overlays.Top())
		}
	})
	okButton.Importance = widget.HighImportance

	content := container.NewVBox(
		widget.NewLabelWithStyle("Fetch v0.1.0", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		container.NewHBox(
			layout.NewSpacer(),
			projectLink,
			layout.NewSpacer(),
		),
		widget.NewLabelWithStyle("released under the Apache 2.0 license", fyne.TextAlignCenter, fyne.TextStyle{}),
		widget.NewLabelWithStyle("© 2026 Ch1ffr3punk", fyne.TextAlignCenter, fyne.TextStyle{}),
		widget.NewLabel(""),
		container.NewHBox(
			layout.NewSpacer(),
			okButton,
			layout.NewSpacer(),
		),
	)

	dialog.ShowCustomWithoutButtons("", content, app.window)
}

func main() {
	flag.Parse()

	myApp := app.NewWithID("oc2mx.net.fetch")
	myApp.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})

	window := myApp.NewWindow("Fetch")
	window.CenterOnScreen()
	window.Resize(fyne.NewSize(550, 500))

	dataDir := getDataDir()
	articlesDir := getArticlesDir()
	configPath := getConfigPath()
	dbPath := getDBPath()

	esubApp := &EsubApp{
		app:         myApp,
		window:      window,
		configPath:  configPath,
		dbPath:      dbPath,
		isDarkTheme: true,
		articlesDir: articlesDir,
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		dialog.ShowError(fmt.Errorf("failed to create data directory: %v", err), window)
		return
	}

	esubApp.initDB()

	fetchBtn := widget.NewButton("Fetch articles", esubApp.fetchArticlesFromNewsgroup)
	fetchBtn.Importance = widget.HighImportance

	displayBtn := widget.NewButton("Display", esubApp.displayArticle)
	displayBtn.Importance = widget.HighImportance

	esubApp.statusLabel = widget.NewLabel("Ready")
	esubApp.statusLabel.Alignment = fyne.TextAlignCenter

	esubApp.progressBar = widget.NewProgressBar()
	esubApp.progressBar.SetValue(0)
	esubApp.progressLabel = widget.NewLabel("0%")
	esubApp.progressLabel.Alignment = fyne.TextAlignCenter

	esubApp.progressContainer = container.NewVBox(
		widget.NewLabel("Progress:"),
		esubApp.progressBar,
		esubApp.progressLabel,
	)
	esubApp.progressContainer.Hide()

	settingsBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), esubApp.showSettings)
	settingsBtn.Importance = widget.LowImportance

	infoBtn := widget.NewButtonWithIcon("", theme.InfoIcon(), esubApp.showInfoPopup)
	infoBtn.Importance = widget.LowImportance

	esubApp.themeSwitch = widget.NewButton(esubApp.getThemeIcon(), esubApp.toggleTheme)
	esubApp.themeSwitch.Importance = widget.LowImportance

	topBar := container.NewHBox(
		settingsBtn,
		layout.NewSpacer(),
		infoBtn,
		layout.NewSpacer(),
		esubApp.themeSwitch,
	)

	buttonContainer := container.NewCenter(
		container.NewHBox(fetchBtn, displayBtn),
	)

	content := container.NewVBox(
		topBar,
		layout.NewSpacer(),
		buttonContainer,
		layout.NewSpacer(),
		esubApp.progressContainer,
		layout.NewSpacer(),
		esubApp.statusLabel,
		container.NewPadded(widget.NewLabel("")),
	)

	window.SetContent(content)
	window.ShowAndRun()
}
