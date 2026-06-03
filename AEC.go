// QR-Code https://medium.com/@ahamrouni/build-a-qr-code-cli-in-go-encode-decode-step-by-step-a279b7f0c671
// NaClbox https://github.com/rovaughn/box - Copyright (c) 2018 Alec Newman <alecnwmn904@gmail.com>

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/url"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/liyue201/goqr"
	"github.com/skip2/go-qrcode"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/oliamb/cutter"
	"golang.org/x/crypto/nacl/box"
)

const targetSize = 344
const maxUserSize = 343
const handleSize float32 = 8.0

type dragState int

const (
	stateIdle dragState = iota
	stateDrawing
	stateMoving
	stateResizing
)

type ImageWithOverlay struct {
	widget.BaseWidget
	imgCanvas   *canvas.Image
	originalImg image.Image

	mode       dragState
	corner     int
	dragOffset fyne.Position
	rectStart  image.Rectangle

	startPos     fyne.Position
	endPos       fyne.Position
	selectedRect image.Rectangle
	hasSelection bool

	touchArea *canvas.Rectangle
	rect      *canvas.Rectangle
	border    *canvas.Rectangle
	handles   [4]*canvas.Rectangle
}

func NewImageWithOverlay(img image.Image) *ImageWithOverlay {
	iwo := &ImageWithOverlay{
		originalImg: img,
	}
	iwo.imgCanvas = canvas.NewImageFromImage(img)
	iwo.imgCanvas.FillMode = canvas.ImageFillContain

	iwo.touchArea = canvas.NewRectangle(color.Transparent)
	iwo.rect = canvas.NewRectangle(color.NRGBA{R: 0, G: 0, B: 255, A: 100})
	iwo.border = canvas.NewRectangle(color.NRGBA{R: 255, G: 255, B: 255, A: 200})
	iwo.border.StrokeWidth = 2
	iwo.border.StrokeColor = color.White

	for i := 0; i < 4; i++ {
		h := canvas.NewRectangle(color.White)
		h.StrokeWidth = 1
		h.StrokeColor = color.Black
		iwo.handles[i] = h
		h.Hidden = true
	}

	iwo.rect.Hidden = true
	iwo.border.Hidden = true

	iwo.ExtendBaseWidget(iwo)
	return iwo
}

func (iwo *ImageWithOverlay) SetImage(img image.Image) {
	iwo.originalImg = img
	iwo.imgCanvas.Image = img
	iwo.imgCanvas.Refresh()
	iwo.Refresh()
}

func (iwo *ImageWithOverlay) hitTest(pos fyne.Position) (dragState, int) {
	if !iwo.hasSelection {
		return stateDrawing, 0
	}

	r := iwo.selectedRect
	rx1, ry1 := float32(r.Min.X), float32(r.Min.Y)
	rx2, ry2 := float32(r.Max.X), float32(r.Max.Y)
	h := handleSize / 2.0

	corners := [4]fyne.Position{
		{rx1, ry1}, {rx2, ry1}, {rx1, ry2}, {rx2, ry2},
	}
	for i, c := range corners {
		if pos.X >= c.X-h && pos.X <= c.X+h && pos.Y >= c.Y-h && pos.Y <= c.Y+h {
			return stateResizing, i
		}
	}

	margin := float32(5)
	if pos.X >= rx1-margin && pos.X <= rx2+margin && pos.Y >= ry1-margin && pos.Y <= ry2+margin {
		return stateMoving, 0
	}

	return stateDrawing, 0
}

func (iwo *ImageWithOverlay) MouseDown(ev *desktop.MouseEvent) {
	iwo.startPos = ev.Position
	iwo.endPos = ev.Position

	iwo.mode, iwo.corner = iwo.hitTest(ev.Position)

	if iwo.mode == stateDrawing {
		iwo.hasSelection = false
	} else if iwo.mode == stateMoving {
		iwo.rectStart = iwo.selectedRect
		iwo.dragOffset = ev.Position.Subtract(fyne.NewPos(float32(iwo.selectedRect.Min.X), float32(iwo.selectedRect.Min.Y)))
	} else if iwo.mode == stateResizing {
		iwo.rectStart = iwo.selectedRect
	}
	iwo.Refresh()
}

func (iwo *ImageWithOverlay) MouseUp(ev *desktop.MouseEvent) {
	if iwo.mode != stateIdle {
		iwo.mode = stateIdle
		if iwo.hasSelection && (iwo.selectedRect.Dx() <= 5 || iwo.selectedRect.Dy() <= 5) {
			iwo.hasSelection = false
		}
	}
	iwo.Refresh()
}

func (iwo *ImageWithOverlay) Dragged(ev *fyne.DragEvent) {
	currentPos := ev.PointEvent.Position
	iwo.endPos = currentPos

	switch iwo.mode {
	case stateDrawing:
		x1 := int(min(iwo.startPos.X, currentPos.X))
		y1 := int(min(iwo.startPos.Y, currentPos.Y))
		x2 := int(max(iwo.startPos.X, currentPos.X))
		y2 := int(max(iwo.startPos.Y, currentPos.Y))
		iwo.selectedRect = image.Rect(x1, y1, x2, y2)
		iwo.hasSelection = iwo.selectedRect.Dx() > 5 && iwo.selectedRect.Dy() > 5

	case stateMoving:
		newX := currentPos.X - iwo.dragOffset.X
		newY := currentPos.Y - iwo.dragOffset.Y
		w := iwo.rectStart.Dx()
		h := iwo.rectStart.Dy()
		iwo.selectedRect = image.Rect(int(newX), int(newY), int(newX)+w, int(newY)+h)

	case stateResizing:
		r := iwo.rectStart
		nx1, ny1 := float32(r.Min.X), float32(r.Min.Y)
		nx2, ny2 := float32(r.Max.X), float32(r.Max.Y)

		switch iwo.corner {
		case 0:
			nx1, ny1 = currentPos.X, currentPos.Y
		case 1:
			nx2, ny1 = currentPos.X, currentPos.Y
		case 2:
			nx1, ny2 = currentPos.X, currentPos.Y
		case 3:
			nx2, ny2 = currentPos.X, currentPos.Y
		}

		iwo.selectedRect = image.Rect(int(min(nx1, nx2)), int(min(ny1, ny2)), int(max(nx1, nx2)), int(max(ny1, ny2)))
		iwo.hasSelection = iwo.selectedRect.Dx() > 5 && iwo.selectedRect.Dy() > 5
	}
	iwo.Refresh()
}

func (iwo *ImageWithOverlay) DragEnd() {}

func (iwo *ImageWithOverlay) GetSelectionRect() (image.Rectangle, bool) {
	return iwo.selectedRect, iwo.hasSelection
}

func (iwo *ImageWithOverlay) ClearSelection() {
	iwo.hasSelection = false
	iwo.mode = stateIdle
	iwo.Refresh()
}

func (iwo *ImageWithOverlay) CreateRenderer() fyne.WidgetRenderer {
	return &imageWithOverlayRenderer{iwo: iwo}
}

type imageWithOverlayRenderer struct {
	iwo *ImageWithOverlay
}

func (r *imageWithOverlayRenderer) Layout(size fyne.Size) {
	r.iwo.imgCanvas.Resize(size)
	r.iwo.touchArea.Resize(size)
}

func (r *imageWithOverlayRenderer) MinSize() fyne.Size {
	return fyne.NewSize(400, 300)
}

func (r *imageWithOverlayRenderer) Refresh() {
	r.iwo.imgCanvas.Refresh()
	r.iwo.touchArea.Refresh()

	show := r.iwo.hasSelection || r.iwo.mode != stateIdle
	if !show {
		r.iwo.rect.Hidden = true
		r.iwo.border.Hidden = true
		for i := range r.iwo.handles {
			r.iwo.handles[i].Hidden = true
		}
		return
	}

	r.iwo.rect.Hidden = false
	r.iwo.border.Hidden = false
	for i := range r.iwo.handles {
		r.iwo.handles[i].Hidden = false
	}

	rect := r.iwo.selectedRect
	r.iwo.rect.Move(fyne.NewPos(float32(rect.Min.X), float32(rect.Min.Y)))
	r.iwo.rect.Resize(fyne.NewSize(float32(rect.Dx()), float32(rect.Dy())))
	r.iwo.border.Move(fyne.NewPos(float32(rect.Min.X), float32(rect.Min.Y)))
	r.iwo.border.Resize(fyne.NewSize(float32(rect.Dx()), float32(rect.Dy())))

	halfH := handleSize / 2.0
	handlesPos := [4]fyne.Position{
		{float32(rect.Min.X) - halfH, float32(rect.Min.Y) - halfH},
		{float32(rect.Max.X) - halfH, float32(rect.Min.Y) - halfH},
		{float32(rect.Min.X) - halfH, float32(rect.Max.Y) - halfH},
		{float32(rect.Max.X) - halfH, float32(rect.Max.Y) - halfH},
	}
	for i := range r.iwo.handles {
		r.iwo.handles[i].Move(handlesPos[i])
		r.iwo.handles[i].Resize(fyne.NewSize(handleSize, handleSize))
		r.iwo.handles[i].Refresh()
	}

	r.iwo.rect.Refresh()
	r.iwo.border.Refresh()
}

func (r *imageWithOverlayRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{
		r.iwo.imgCanvas,
		r.iwo.rect,
		r.iwo.border,
		r.iwo.handles[0], r.iwo.handles[1], r.iwo.handles[2], r.iwo.handles[3],
		r.iwo.touchArea,
	}
}

func (r *imageWithOverlayRenderer) Destroy() {}

func min(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func max(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

type entity struct {
	pub, sec *[32]byte
	name     string
}

func (e *entity) isIdentity() bool { return e.pub != nil && e.sec != nil }
func (e *entity) isPeer() bool     { return e.pub != nil && e.sec == nil }

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9-_]+$`)

type purpleTheme struct {
	base fyne.Theme
}

func (g *purpleTheme) Font(s fyne.TextStyle) fyne.Resource {
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

func aecDir() string {
	if d := os.Getenv("AECDIR"); d != "" {
		return d
	}
	usr, _ := user.Current()
	return path.Join(usr.HomeDir, ".AEC")
}

func loadEntity(pathStr string) (*entity, error) {
	data, err := os.ReadFile(pathStr)
	if err != nil {
		return nil, err
	}
	e := &entity{name: filepath.Base(pathStr)}
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		switch block.Type {
		case "AEC SECRET KEY":
			e.sec = new([32]byte)
			copy(e.sec[:], block.Bytes)
		case "AEC PUBLIC KEY":
			e.pub = new([32]byte)
			copy(e.pub[:], block.Bytes)
		}
		data = rest
	}
	return e, nil
}

func listIdentities() ([]string, error) {
	dir := aecDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var identities []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		entity, err := loadEntity(filepath.Join(dir, entry.Name()))
		if err == nil && entity.isIdentity() {
			identities = append(identities, entry.Name())
		}
	}
	return identities, nil
}

func listPeers() ([]string, error) {
	dir := aecDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var peers []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		entity, err := loadEntity(filepath.Join(dir, entry.Name()))
		if err == nil && entity.isPeer() {
			peers = append(peers, entry.Name())
		}
	}
	return peers, nil
}

func addISOPadding(data []byte, targetLen int) ([]byte, error) {
	if len(data) >= targetLen {
		return nil, fmt.Errorf("data too long (max %d bytes)", targetLen-1)
	}
	padded := make([]byte, targetLen)
	copy(padded, data)
	padded[len(data)] = 0x80
	return padded, nil
}

func removeISOPadding(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == 0x80 {
			return data[:i], nil
		}
		if data[i] != 0 {
			return nil, fmt.Errorf("invalid padding")
		}
	}
	return nil, fmt.Errorf("invalid padding: 0x80 not found")
}

func encodeArmorByte(input byte, secondChannel bool) rune {
	switch {
	case input <= 9 && secondChannel:
		return rune(input + 'A')
	case input <= 9:
		return rune(input + 'Q')
	case input >= 10 && input <= 15:
		return rune(input - 10 + 'K')
	default:
		panic("Invalid input")
	}
}

func encodeArmor(input []byte) string {
	var result strings.Builder
	secondChannel := false

	for _, b := range input {
		high := b >> 4
		low := b & 0x0F

		encodedHigh := encodeArmorByte(high, secondChannel)
		encodedLow := encodeArmorByte(low, !secondChannel)

		result.WriteRune(encodedHigh)
		result.WriteRune(encodedLow)

		secondChannel = !secondChannel
	}

	return result.String()
}

func decodeArmor(input string) []byte {
	var result []byte

	cleaned := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, input)

	for i := 0; i < len(cleaned); i += 2 {
		if i+1 >= len(cleaned) {
			break
		}

		highChar := rune(cleaned[i])
		lowChar := rune(cleaned[i+1])

		decodeRune := func(r rune) (byte, bool) {
			switch {
			case r >= 'A' && r <= 'J':
				return byte(r - 'A'), false
			case r >= 'Q' && r <= 'Z':
				return byte(r - 'Q'), true
			case r >= 'K' && r <= 'P':
				return byte(r-'K') + 10, false
			default:
				return 0, false
			}
		}

		decodedHigh, _ := decodeRune(highChar)
		decodedLow, _ := decodeRune(lowChar)

		result = append(result, decodedHigh<<4|decodedLow)
	}

	return result
}

func encodeQR(data []byte, size int) ([]byte, error) {
	qr, err := qrcode.New(string(data), qrcode.Low)
	if err != nil {
		return nil, fmt.Errorf("failed to create QR code: %w", err)
	}
	qr.DisableBorder = false
	img := qr.Image(size)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("failed to encode PNG: %w", err)
	}
	return buf.Bytes(), nil
}

func decodeQR(input io.Reader) ([]byte, error) {
	img, _, err := image.Decode(input)
	if err != nil {
		return nil, fmt.Errorf("failed to decode PNG: %w", err)
	}

	codes, err := goqr.Recognize(img)
	if err != nil {
		return nil, fmt.Errorf("failed to recognize QR: %w", err)
	}
	if len(codes) == 0 {
		return nil, errors.New("no QR code found in image")
	}

	return codes[0].Payload, nil
}

func isQRCode(input []byte) bool {
	return len(input) > 8 && string(input[0:8]) == "\x89PNG\r\n\x1a\n"
}

func cropQRImage(img image.Image, cropRect image.Rectangle, viewSize fyne.Size) (image.Image, error) {
	imgBounds := img.Bounds()
	wRatio := float64(viewSize.Width) / float64(imgBounds.Dx())
	hRatio := float64(viewSize.Height) / float64(imgBounds.Dy())

	scale := wRatio
	if hRatio < wRatio {
		scale = hRatio
	}

	displayedW := float64(imgBounds.Dx()) * scale
	displayedH := float64(imgBounds.Dy()) * scale

	offsetX := (float64(viewSize.Width) - displayedW) / 2.0
	offsetY := (float64(viewSize.Height) - displayedH) / 2.0

	cropMinX := float64(cropRect.Min.X) - offsetX
	cropMinY := float64(cropRect.Min.Y) - offsetY

	realScale := 1.0 / scale
	realX := int(cropMinX * realScale)
	realY := int(cropMinY * realScale)
	realW := int(float64(cropRect.Dx()) * realScale)
	realH := int(float64(cropRect.Dy()) * realScale)

	if realX < 0 {
		realX = 0
	}
	if realY < 0 {
		realY = 0
	}
	if realX+realW > imgBounds.Dx() {
		realW = imgBounds.Dx() - realX
	}
	if realY+realH > imgBounds.Dy() {
		realH = imgBounds.Dy() - realY
	}

	if realW <= 5 || realH <= 5 {
		return nil, fmt.Errorf("Selection too small to crop")
	}

	return cutter.Crop(img, cutter.Config{
		Width:  realW,
		Height: realH,
		Anchor: image.Point{X: realX, Y: realY},
		Mode:   cutter.TopLeft,
	})
}

type AECApp struct {
	app           fyne.App
	window        fyne.Window
	textArea      *widget.Entry
	statusLabel   *widget.Label
	charCounter   *widget.Label
	isDarkTheme   bool
	themeSwitch   *widget.Button
	infoBtn       *widget.Button
	scannedPubKey string
}

func (a *AECApp) updateCharCount() {
	text := a.textArea.Text
	byteCount := len([]byte(text))
	a.charCounter.SetText(fmt.Sprintf("%d/%d", byteCount, maxUserSize))

	if byteCount > maxUserSize {
		a.charCounter.TextStyle = fyne.TextStyle{Bold: true}
	} else {
		a.charCounter.TextStyle = fyne.TextStyle{}
	}
	a.charCounter.Refresh()
}

func (a *AECApp) clearText() {
	a.textArea.SetText("")
	a.updateCharCount()
	a.statusLabel.SetText("Text cleared")
}

func (a *AECApp) toggleTheme() {
	if a.isDarkTheme {
		a.app.Settings().SetTheme(&purpleTheme{base: theme.LightTheme()})
		a.isDarkTheme = false
		a.themeSwitch.SetText("🌙")
	} else {
		a.app.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})
		a.isDarkTheme = true
		a.themeSwitch.SetText("☀️")
	}
}

func (a *AECApp) showInfoPopup() {
	projURL, _ := url.Parse("https://github.com/Ch1ffr3punk/AEC")
	projectLink := widget.NewHyperlink("An Open Source project", projURL)
	okButton := widget.NewButton("OK", func() {
		if overlays := a.window.Canvas().Overlays(); overlays.Top() != nil {
			overlays.Remove(overlays.Top())
		}
	})
	okButton.Importance = widget.HighImportance
	content := container.NewVBox(
		widget.NewLabelWithStyle("AEC v0.1.0", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
        container.NewHBox(layout.NewSpacer(), projectLink, layout.NewSpacer()),
        widget.NewLabelWithStyle("released under the Apache 2.0 license", fyne.TextAlignCenter, fyne.TextStyle{}),
        widget.NewLabelWithStyle("© 2026 Ch1ffr3punk", fyne.TextAlignCenter, fyne.TextStyle{}),
        container.NewHBox(layout.NewSpacer(), okButton, layout.NewSpacer()),
	)
	dialog.ShowCustomWithoutButtons("", content, a.window)
}

func (a *AECApp) showQRPopup(qrData []byte, title string) {
	img, _, err := image.Decode(bytes.NewReader(qrData))
	if err != nil {
		dialog.ShowError(fmt.Errorf("failed to decode QR: %v", err), a.window)
		return
	}

	imgWidget := canvas.NewImageFromImage(img)
	imgWidget.SetMinSize(fyne.NewSize(512, 512))
	imgWidget.FillMode = canvas.ImageFillOriginal

	saveBtn := widget.NewButton("Save", func() {
		dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
			if writer != nil {
				defer writer.Close()
				writer.Write(qrData)
				a.statusLabel.SetText("QR saved")
			}
		}, a.window)
	})
	saveBtn.Importance = widget.HighImportance

	okBtn := widget.NewButton("OK", func() {
		for _, o := range a.window.Canvas().Overlays().List() {
			a.window.Canvas().Overlays().Remove(o)
		}
	})
	okBtn.Importance = widget.HighImportance

	buttonRow := container.NewHBox(layout.NewSpacer(), saveBtn, okBtn, layout.NewSpacer())
	content := container.NewVBox(
		widget.NewLabelWithStyle(title, fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		imgWidget,
		buttonRow,
	)

	popup := widget.NewModalPopUp(content, a.window.Canvas())
	popup.Resize(fyne.NewSize(550, 650))
	popup.Show()
}

func (a *AECApp) cmdInit() {
	dialog.ShowEntryDialog("Enter identity name", "self", func(name string) {
		if name == "" {
			return
		}

		if !nameRegex.MatchString(name) {
			dialog.ShowError(fmt.Errorf("invalid name: %s", name), a.window)
			return
		}

		pub, sec, err := box.GenerateKey(rand.Reader)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}

		dir := aecDir()
		if err := os.MkdirAll(dir, 0700); err != nil {
			dialog.ShowError(err, a.window)
			return
		}

		f, err := os.OpenFile(path.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		defer f.Close()

		for _, b := range []*pem.Block{
			{Type: "AEC SECRET KEY", Bytes: sec[:]},
			{Type: "AEC PUBLIC KEY", Bytes: pub[:]},
		} {
			if err := pem.Encode(f, b); err != nil {
				dialog.ShowError(err, a.window)
				return
			}
		}

		pubHex := hex.EncodeToString(pub[:])
		
		fullQRData, err := encodeQR([]byte(pubHex), 512)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		
		displayQRData, err := encodeQR([]byte(pubHex), 448)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		
		displayQRImg, _, err := image.Decode(bytes.NewReader(displayQRData))
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		
		qrWidget := canvas.NewImageFromImage(displayQRImg)
		qrWidget.SetMinSize(fyne.NewSize(448, 448))
		qrWidget.FillMode = canvas.ImageFillOriginal
		
		saveQRBtn := widget.NewButton("Save QR Code", func() {
			dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
				if writer != nil {
					defer writer.Close()
					writer.Write(fullQRData)
					a.statusLabel.SetText("QR code saved (512x512)")
				}
			}, a.window)
		})
		saveQRBtn.Importance = widget.HighImportance
		
		okBtn := widget.NewButton("OK", func() {
			for _, o := range a.window.Canvas().Overlays().List() {
				a.window.Canvas().Overlays().Remove(o)
			}
		})
		
		dialogContent := container.NewVBox(
			widget.NewLabelWithStyle(fmt.Sprintf("Identity '%s' created successfully", name), fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			widget.NewLabelWithStyle("Public Key QR Code", fyne.TextAlignCenter, fyne.TextStyle{}),
			qrWidget,
			container.NewHBox(layout.NewSpacer(), saveQRBtn, layout.NewSpacer(), okBtn, layout.NewSpacer()),
		)
		
		popup := widget.NewModalPopUp(dialogContent, a.window.Canvas())
		popup.Resize(fyne.NewSize(520, 620))
		popup.Show()
		
		a.statusLabel.SetText(fmt.Sprintf("Identity '%s' created", name))
	}, a.window)
}

func (a *AECApp) cmdRecover() {
	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if reader != nil {
			defer reader.Close()

			data, err := io.ReadAll(reader)
			if err != nil {
				dialog.ShowError(err, a.window)
				return
			}

			img, format, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				dialog.ShowError(fmt.Errorf("failed to decode image: %w\nSupported formats: PNG, JPEG, GIF, etc.", err), a.window)
				return
			}

			fmt.Printf("Loaded format: %s\n", format)

			cropWindow := a.app.NewWindow("Crop QR Code")
			cropWindow.Resize(fyne.NewSize(800, 600))
			cropWindow.CenterOnScreen()

			imageWidget := NewImageWithOverlay(img)

			var croppedImg image.Image
			cropConfirmed := false

			cropBtn := widget.NewButton("Apply Crop", func() {
				rect, ok := imageWidget.GetSelectionRect()
				if !ok {
					dialog.ShowInformation("No Selection", "Please draw a selection rectangle on the image first.", cropWindow)
					return
				}

				newImg, err := cropQRImage(img, rect, imageWidget.Size())
				if err != nil {
					dialog.ShowError(err, cropWindow)
					return
				}

				croppedImg = newImg
				cropConfirmed = true
				imageWidget.SetImage(croppedImg)
				imageWidget.ClearSelection()
				dialog.ShowInformation("Success", "Image cropped successfully!", cropWindow)
			})

			resetBtn := widget.NewButton("Reset", func() {
				croppedImg = nil
				cropConfirmed = false
				imageWidget.SetImage(img)
				imageWidget.ClearSelection()
			})

			confirmBtn := widget.NewButton("Confirm & Recover", func() {
				var finalImg image.Image
				if cropConfirmed && croppedImg != nil {
					finalImg = croppedImg
				} else {
					finalImg = img
				}

				var buf bytes.Buffer
				if err := png.Encode(&buf, finalImg); err != nil {
					dialog.ShowError(fmt.Errorf("failed to encode image as PNG: %w", err), cropWindow)
					return
				}

				recoveredData, err := decodeQR(&buf)
				if err != nil {
					dialog.ShowError(fmt.Errorf("failed to decode QR from image: %w\nPlease make sure the image contains a QR code", err), cropWindow)
					return
				}

				recoveredString := string(recoveredData)
				
				if len(recoveredData) == 64 || (len(recoveredString) == 64 && regexp.MustCompile(`^[a-fA-F0-9]+$`).MatchString(recoveredString)) {
					qrData, err := encodeQR(recoveredData, 512)
					if err != nil {
						dialog.ShowError(err, cropWindow)
						return
					}
					
					cropWindow.Close()
					a.showQRPopup(qrData, "Recovered Public Key QR Code")
					a.statusLabel.SetText("Public key QR recovered successfully")
				} else if len(recoveredData) == 768 {
					qrData, err := encodeQR(recoveredData, 512)
					if err != nil {
						dialog.ShowError(err, cropWindow)
						return
					}

					cropWindow.Close()
					a.showQRPopup(qrData, "Recovered Encrypted QR Code")
					a.statusLabel.SetText("QR recovered and cropped successfully")
				} else {
					dialog.ShowError(fmt.Errorf("recovered data has unexpected size: %d bytes (expected 64 bytes for public key or 768 bytes for encrypted data)", len(recoveredData)), cropWindow)
					return
				}
			})

			cancelBtn := widget.NewButton("Cancel", func() {
				cropWindow.Close()
			})

			cropBtn.Importance = widget.HighImportance
			resetBtn.Importance = widget.MediumImportance
			confirmBtn.Importance = widget.HighImportance
			cancelBtn.Importance = widget.MediumImportance

			buttonBar := container.NewHBox(
				layout.NewSpacer(),
				cropBtn,
				resetBtn,
				confirmBtn,
				cancelBtn,
				layout.NewSpacer(),
			)

			content := container.NewBorder(
				container.NewVBox(
					widget.NewLabelWithStyle("Drag to select the QR code", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
					widget.NewSeparator(),
				),
				buttonBar,
				nil,
				nil,
				imageWidget,
			)

			cropWindow.SetContent(content)
			cropWindow.Show()
		}
	}, a.window)
}

func (a *AECApp) cmdAdd() {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("peer name")
	
	loadQRBtn := widget.NewButton("Load QR Code PNG", func() {
		dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				defer reader.Close()
				
				data, err := io.ReadAll(reader)
				if err != nil {
					dialog.ShowError(err, a.window)
					return
				}
				
				if !isQRCode(data) {
					dialog.ShowError(fmt.Errorf("selected file is not a QR code PNG"), a.window)
					return
				}
				
				armored, err := decodeQR(bytes.NewReader(data))
				if err != nil {
					dialog.ShowError(fmt.Errorf("failed to decode QR: %w", err), a.window)
					return
				}
				
				pubKeyHex := string(armored)
				
				if len(pubKeyHex) != 64 {
					dialog.ShowError(fmt.Errorf("invalid public key (got %d chars, expected 64)", len(pubKeyHex)), a.window)
					return
				}
				
				a.scannedPubKey = pubKeyHex
				
				if nameEntry.Text == "" {
					nameEntry.SetText("peer_" + pubKeyHex[:8])
				}
				
				a.statusLabel.SetText("Public key loaded from QR code")
				dialog.ShowInformation("Success", "Public key loaded from QR code.\nYou can now add the peer.", a.window)
			}
		}, a.window)
	})
	loadQRBtn.Importance = widget.HighImportance
	
	items := []*widget.FormItem{
		{Text: "Name", Widget: nameEntry},
		{Text: "Load QR", Widget: loadQRBtn},
	}
	
	dialog.ShowForm("Add Peer", "Add", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}
		
		name := strings.TrimSpace(nameEntry.Text)
		
		if name == "" {
			dialog.ShowError(fmt.Errorf("name required"), a.window)
			return
		}
		
		if !nameRegex.MatchString(name) {
			dialog.ShowError(fmt.Errorf("invalid name: %s", name), a.window)
			return
		}
		
		if a.scannedPubKey == "" {
			dialog.ShowError(fmt.Errorf("no public key loaded. Please load a QR code first"), a.window)
			return
		}
		
		keyHex := a.scannedPubKey
		key, err := hex.DecodeString(keyHex)
		if err != nil || len(key) != 32 {
			dialog.ShowError(fmt.Errorf("invalid public key (need 32 bytes)"), a.window)
			return
		}
		
		dir := aecDir()
		if err := os.MkdirAll(dir, 0700); err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		
		f, err := os.OpenFile(path.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		defer f.Close()
		
		if err := pem.Encode(f, &pem.Block{Type: "AEC PUBLIC KEY", Bytes: key}); err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		
		a.scannedPubKey = ""
		a.statusLabel.SetText(fmt.Sprintf("Peer '%s' added", name))
	}, a.window)
}

func (a *AECApp) cmdEncrypt() {
	text := a.textArea.Text
	if text == "" {
		dialog.ShowError(fmt.Errorf("no text to encrypt"), a.window)
		return
	}

	byteCount := len([]byte(text))
	if byteCount > maxUserSize {
		dialog.ShowError(fmt.Errorf("text too long: %d/%d bytes", byteCount, maxUserSize), a.window)
		return
	}

	identities, err := listIdentities()
	if err != nil || len(identities) == 0 {
		dialog.ShowError(fmt.Errorf("no identities found. Create one with Init"), a.window)
		return
	}

	peers, err := listPeers()
	if err != nil || len(peers) == 0 {
		dialog.ShowError(fmt.Errorf("no peers found. Add one with Add"), a.window)
		return
	}

	senderSelect := widget.NewSelect(identities, func(selected string) {})
	senderSelect.SetSelected(identities[0])

	recipientSelect := widget.NewSelect(peers, func(selected string) {})
	recipientSelect.SetSelected(peers[0])

	items := []*widget.FormItem{
		{Text: "Your Identity", Widget: senderSelect},
		{Text: "Recipient", Widget: recipientSelect},
	}

	dialog.ShowForm("Encrypt", "Encrypt", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}

		from := senderSelect.Selected
		to := recipientSelect.Selected

		dir := aecDir()
		sender, err := loadEntity(path.Join(dir, from))
		if err != nil || sender.sec == nil {
			dialog.ShowError(fmt.Errorf("invalid sender identity"), a.window)
			return
		}

		recv, err := loadEntity(path.Join(dir, to))
		if err != nil || recv.pub == nil {
			dialog.ShowError(fmt.Errorf("invalid recipient public key"), a.window)
			return
		}

		msg := []byte(text)
		paddedMsg, err := addISOPadding(msg, targetSize)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}

		var nonce [24]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			dialog.ShowError(err, a.window)
			return
		}

		encrypted := box.Seal(nil, paddedMsg, &nonce, recv.pub, sender.sec)
		ciphertext := append(nonce[:], encrypted...)
		armored := encodeArmor(ciphertext)

		qrData, err := encodeQR([]byte(armored), 512)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}

		a.showQRPopup(qrData, "Encrypted QR Code")
		a.statusLabel.SetText("Encrypted")
	}, a.window)
}

func (a *AECApp) cmdDecrypt() {
	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if reader != nil {
			defer reader.Close()

			data, err := io.ReadAll(reader)
			if err != nil {
				dialog.ShowError(err, a.window)
				return
			}

			if !isQRCode(data) {
				dialog.ShowError(fmt.Errorf("selected file is not a QR code PNG"), a.window)
				return
			}

			identities, err := listIdentities()
			if err != nil || len(identities) == 0 {
				dialog.ShowError(fmt.Errorf("no identities found. Create one with Init"), a.window)
				return
			}

			peers, err := listPeers()
			if err != nil {
				dialog.ShowError(err, a.window)
				return
			}

			armored, err := decodeQR(bytes.NewReader(data))
			if err != nil {
				dialog.ShowError(err, a.window)
				return
			}

			ciphertext := decodeArmor(string(armored))
			if len(ciphertext) < 24 {
				dialog.ShowError(fmt.Errorf("ciphertext too short"), a.window)
				return
			}

			var nonce [24]byte
			copy(nonce[:24], ciphertext[:24])
			encrypted := ciphertext[24:]

			var decryptedMsg []byte
			found := false

			dir := aecDir()

OuterLoop:
			for _, identityName := range identities {
				myIdentity, err := loadEntity(path.Join(dir, identityName))
				if err != nil || myIdentity.sec == nil {
					continue
				}

				for _, peerName := range peers {
					senderPeer, err := loadEntity(path.Join(dir, peerName))
					if err != nil || senderPeer.pub == nil {
						continue
					}

					paddedMsg, ok := box.Open(nil, encrypted, &nonce, senderPeer.pub, myIdentity.sec)
					if ok {
						decryptedMsg = paddedMsg
						found = true
						break OuterLoop
					}
				}

				if myIdentity.pub != nil {
					paddedMsg, ok := box.Open(nil, encrypted, &nonce, myIdentity.pub, myIdentity.sec)
					if ok {
						decryptedMsg = paddedMsg
						found = true
						break OuterLoop
					}
				}
			}

			if !found {
				dialog.ShowError(errors.New("decryption failed: Unknown key pair or corrupted message"), a.window)
				return
			}

			msg, err := removeISOPadding(decryptedMsg)
			if err != nil {
				dialog.ShowError(err, a.window)
				return
			}

			a.textArea.SetText(string(msg))
			a.updateCharCount()
			a.statusLabel.SetText("Decryption successful")
		}
	}, a.window)
}

func (a *AECApp) setupUI() {
	a.textArea = widget.NewMultiLineEntry()
	a.textArea.SetPlaceHolder(fmt.Sprintf("Enter text here (max %d bytes)...", maxUserSize))
	a.textArea.Wrapping = fyne.TextWrapWord
	a.textArea.OnChanged = func(string) {
		a.updateCharCount()
	}

	a.statusLabel = widget.NewLabel("Ready")
	a.charCounter = widget.NewLabel(fmt.Sprintf("0/%d", maxUserSize))

	statusBar := container.NewVBox(
		widget.NewSeparator(),
		container.NewHBox(
			a.statusLabel,
			layout.NewSpacer(),
			a.charCounter,
		),
	)

	a.themeSwitch = widget.NewButton("☀️", a.toggleTheme)
	a.themeSwitch.Importance = widget.LowImportance
	a.infoBtn = widget.NewButtonWithIcon("", theme.InfoIcon(), a.showInfoPopup)
	a.infoBtn.Importance = widget.LowImportance

	topBar := container.NewHBox(layout.NewSpacer(), a.infoBtn, layout.NewSpacer(), a.themeSwitch)

	btnInit := widget.NewButton("Init", a.cmdInit)
	btnRecover := widget.NewButton("Recover", a.cmdRecover)
	btnAdd := widget.NewButton("Add", a.cmdAdd)
	
	btnEncrypt := widget.NewButton("Encrypt", a.cmdEncrypt)
	btnDecrypt := widget.NewButton("Decrypt", a.cmdDecrypt)
	btnClear := widget.NewButton("Clear", a.clearText)

	for _, btn := range []*widget.Button{btnInit, btnAdd, btnRecover, btnEncrypt, btnDecrypt, btnClear} {
		btn.Importance = widget.HighImportance
	}

	row1 := container.New(layout.NewGridLayoutWithColumns(3), btnInit, btnRecover, btnAdd)
	row2 := container.New(layout.NewGridLayoutWithColumns(3), btnEncrypt, btnDecrypt, btnClear)

	buttonContainer := container.NewVBox(row1, row2)

	content := container.NewBorder(
		container.NewVBox(topBar, widget.NewSeparator(), buttonContainer),
		statusBar,
		nil,
		nil,
		a.textArea,
	)

	a.window.SetContent(content)
}

func main() {
	myApp := app.New()
	myApp.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})

	window := myApp.NewWindow("AEC")
	window.Resize(fyne.NewSize(800, 600))
	window.CenterOnScreen()

	aecApp := &AECApp{
		app:         myApp,
		window:      window,
		isDarkTheme: true,
	}

	aecApp.setupUI()
	aecApp.updateCharCount()

	window.ShowAndRun()
}
