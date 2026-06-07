package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha3"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"net/url"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20"
)

type esub struct {
	key string
}

func (e *esub) deriveKey() []byte {
	pepper := []byte("fixed-pepper-1234")
	key := argon2.IDKey(
		[]byte(e.key),
		pepper,
		3,
		64*1024,
		4,
		32,
	)
	return key
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

type noClipboardEntry struct {
	widget.Entry
}

func (e *noClipboardEntry) TypedShortcut(shortcut fyne.Shortcut) {
	switch shortcut.(type) {
	case *fyne.ShortcutCopy, *fyne.ShortcutPaste, *fyne.ShortcutCut:
		return
	default:
		e.Entry.TypedShortcut(shortcut)
	}
}

func (e *noClipboardEntry) MouseDown(ev *desktop.MouseEvent) {
	if ev.Button == desktop.MouseButtonSecondary {
		return
	}
	e.Entry.MouseDown(ev)
}

type esubGUI struct {
	app         fyne.App
	window      fyne.Window
	keyEntry    *noClipboardEntry
	qrImage     *canvas.Image
	statusLabel *widget.Label
	isDarkTheme bool
	themeSwitch *widget.Button
	infoBtn     *widget.Button
}

func (g *esubGUI) getThemeIcon() string {
	if g.isDarkTheme {
		return "☀️"
	}
	return "🌙"
}

func (g *esubGUI) generateQR() {
	defer func() {
		if r := recover(); r != nil {
			g.statusLabel.SetText("Error: Generation failed")
		}
	}()

	key := g.keyEntry.Text
	if key == "" {
		g.statusLabel.SetText("Error: Please enter a key")
		return
	}

	e := &esub{key: key}
	esubValue := e.esubgen()

	qr, err := qrcode.New(esubValue, qrcode.Medium)
	if err != nil {
		g.statusLabel.SetText("Error generating QR code")
		return
	}
	qr.DisableBorder = false

	img := qr.Image(400)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		g.statusLabel.SetText("Error encoding QR code")
		return
	}

	qrImg, _, err := image.Decode(&buf)
	if err != nil {
		g.statusLabel.SetText("Error displaying QR code")
		return
	}

	g.qrImage.Image = qrImg
	g.qrImage.Refresh()

	g.statusLabel.SetText("QR Code generated successfully")
}

func (g *esubGUI) clearAll() {
	g.keyEntry.SetText("")
	g.qrImage.Image = nil
	g.qrImage.Refresh()
	g.statusLabel.SetText("Cleared - Ready for new input")
}

func (g *esubGUI) saveQR() {
	if g.qrImage.Image == nil {
		dialog.ShowInformation("No QR Code", "Please generate a QR code first", g.window)
		return
	}

	dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
		if writer != nil {
			defer writer.Close()

			if err := png.Encode(writer, g.qrImage.Image); err != nil {
				dialog.ShowError(err, g.window)
				return
			}
			g.statusLabel.SetText("QR Code saved successfully")
		}
	}, g.window)
}

func (g *esubGUI) toggleTheme() {
	if g.isDarkTheme {
		g.app.Settings().SetTheme(&purpleTheme{base: theme.LightTheme()})
		g.isDarkTheme = false
	} else {
		g.app.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})
		g.isDarkTheme = true
	}
	g.themeSwitch.SetText(g.getThemeIcon())
	g.themeSwitch.Refresh()
	g.window.Content().Refresh()
}

func (g *esubGUI) showInfoPopup() {
	projURL, _ := url.Parse("https://github.com/Ch1ffr3punk/AEC")
	projectLink := widget.NewHyperlink("An Open Source project", projURL)
	okButton := widget.NewButton("OK", func() {
		if overlays := g.window.Canvas().Overlays(); overlays.Top() != nil {
			overlays.Remove(overlays.Top())
		}
	})
	okButton.Importance = widget.HighImportance
	content := container.NewVBox(
		widget.NewLabelWithStyle("E2Q v0.1.0", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		container.NewHBox(layout.NewSpacer(), projectLink, layout.NewSpacer()),
		widget.NewLabelWithStyle("released under the Apache 2.0 license", fyne.TextAlignCenter, fyne.TextStyle{}),
		widget.NewLabelWithStyle("© 2026 Ch1ffr3punk", fyne.TextAlignCenter, fyne.TextStyle{}),
		container.NewHBox(layout.NewSpacer(), okButton, layout.NewSpacer()),
	)
	dialog.ShowCustomWithoutButtons("", content, g.window)
}

func (g *esubGUI) setupUI() {
	title := widget.NewLabelWithStyle("Esub to QR-Code", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})

	g.keyEntry = &noClipboardEntry{}
	g.keyEntry.ExtendBaseWidget(g.keyEntry)
	g.keyEntry.SetPlaceHolder("Enter your key here...")

	generateBtn := widget.NewButton("Generate", g.generateQR)
	generateBtn.Importance = widget.HighImportance

	saveBtn := widget.NewButton("Save", g.saveQR)
	saveBtn.Importance = widget.HighImportance

	clearBtn := widget.NewButton("Clear", g.clearAll)
	clearBtn.Importance = widget.HighImportance

	g.infoBtn = widget.NewButtonWithIcon("", theme.InfoIcon(), g.showInfoPopup)
	g.infoBtn.Importance = widget.LowImportance

	g.themeSwitch = widget.NewButton(g.getThemeIcon(), g.toggleTheme)
	g.themeSwitch.Importance = widget.LowImportance

	topBar := container.NewHBox(
		layout.NewSpacer(),
		g.infoBtn,
		layout.NewSpacer(),
		g.themeSwitch,
	)

	leftButtons := container.NewHBox(generateBtn, saveBtn)
	rightButtons := container.NewHBox(clearBtn)

	buttonContainer := container.NewHBox(
		leftButtons,
		layout.NewSpacer(),
		rightButtons,
	)

	g.qrImage = canvas.NewImageFromResource(nil)
	g.qrImage.SetMinSize(fyne.NewSize(400, 400))
	g.qrImage.FillMode = canvas.ImageFillOriginal

	qrContainer := container.NewCenter(g.qrImage)

	g.statusLabel = widget.NewLabelWithStyle("Ready", fyne.TextAlignCenter, fyne.TextStyle{})

	content := container.NewVBox(
		topBar,
		title,
		widget.NewSeparator(),
		g.keyEntry,
		widget.NewSeparator(),
		qrContainer,
		widget.NewSeparator(),
		container.NewCenter(buttonContainer),
		container.NewCenter(g.statusLabel),
	)

	g.window.SetContent(content)
}

func main() {
	myApp := app.New()
	myApp.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})

	window := myApp.NewWindow("E2Q")
	window.Resize(fyne.NewSize(500, 600))
	window.CenterOnScreen()

	gui := &esubGUI{
		app:         myApp,
		window:      window,
		isDarkTheme: true,
	}

	gui.setupUI()
	window.ShowAndRun()
}
