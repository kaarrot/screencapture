// A screen capture and annotation tool.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/ewmh"     // full screen
	"github.com/BurntSushi/xgbutil/xwindow"

	"github.com/BurntSushi/xgbutil/keybind"
	"github.com/BurntSushi/xgbutil/mousebind"

	"github.com/BurntSushi/xgbutil/xevent"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"

	"github.com/BurntSushi/xgbutil/xgraphics" // painting
	"github.com/BurntSushi/xgbutil/xprop"     // atom name
)

type Settings struct {
	brush_size      int
	undos           [][]byte  // undos are stored here
	clipboard       []byte    // this is what gets copied into clipboard
	incr            bool      // a toggle to control when INCR mode is used
	incr_pos        int       // start of the slice to send
	incr_next_pos   int       // end of the slice to send
	incr_last_round bool    // signal the last round which sends 0 length property
	incr_chunk_length int   // threshold to enable INCR protocol - needs to be smaller then max size of X11 property ~250kB
	incr_win          xproto.Window // temporary window and property used between
	incr_pty          xproto.Atom   // selection requests and notification requests
}

// Global Settings - TODO - use init() function to set it up
var SETTINGS = Settings{
	brush_size:        2,
	undos:             make([][]byte, 0),
	incr:              false,
	incr_pos:          0,
	incr_next_pos:     100000,
	incr_last_round:   false,
	incr_chunk_length: 200000}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Returns closed range from the given start and the end values
// 1-5 -> [1,2,3,4,5]
func makeRange(min, max, skip int) []int {
	var reversed = false
	if min > max {
		reversed = true
		min, max = max, min
	}
	a := make([]int, max-min+1)

	counter := 0
	for i := range a {
		a[i] = min + counter
		counter += skip
	}

	if reversed == false {
		return a
	}
	for i, j := 0, len(a)-1; i < j; i, j = i+1, j-1 {
		a[i], a[j] = a[j], a[i]
	}
	return a
}

// Given two ranges extend shorter ones by 'filling in' in-between values
// to match the longer range
func extendRange(x_range []int, y_range []int) ([]int, []int) {
	if len(x_range) == len(y_range) {
		return x_range, y_range
	}
	if len(x_range) > len(y_range) {
		insert_times := len(x_range) - len(y_range)

		for i := 0; i < insert_times; i++ {
			// fmt.Println("before", y_range)
			var index int
			if len(y_range) == 1 {
				index = 0
				y_range = append(y_range[:index+1], y_range[index:]...)
				y_range[index+1] = y_range[index]
				continue
			}
			index = rand.Intn(len(y_range)-1) + 1 // use range[1, total) instead [0, total)
			// fmt.Println(index)
			y_range = append(y_range[:index+1], y_range[index:]...)
			y_range[index] = y_range[index-1] // use previous value
			// fmt.Println("after", y_range)
		}
		return x_range, y_range
	} else if len(y_range) > len(x_range) {
		insert_times := len(y_range) - len(x_range)

		for i := 0; i < insert_times; i++ {
			var index int
			if len(x_range) == 1 {
				index = 0
				x_range = append(x_range[:index+1], x_range[index:]...)
				x_range[index+1] = x_range[index]
				continue
			}
			index = rand.Intn(len(x_range)-1) + 1 // use range[1, total) instead [0, total)

			x_range = append(x_range[:index+1], x_range[index:]...)
			x_range[index] = x_range[index-1] // use previous value
		}
		return x_range, y_range
	}

	return x_range, y_range

	// I always forget this!!!
	// insert elements into a slice
	// a:=[]int{1,2,3,4}
	// a=append(a[:2 + 1], a[2:]...)
	// a[2] = 10
	// fmt.Println(a)

}

// Takes the selection and update byte buffer in the global SETTINGS variable
// It also save a copy into TMP for future reference - in needed
func copyToClipboard(canvas *xgraphics.Image, bounds image.Rectangle) {
	fmt.Println("use this bounds to copy clipboard", bounds)

	buf := new(bytes.Buffer)
	img := canvas.SubImage(bounds)
	encoder := &png.Encoder{CompressionLevel: png.BestCompression}
	err := encoder.Encode(buf, img)
	if err != nil {
		fmt.Println(err)
	}

	file_name := time.Now().Format("2006-01-02_Mon_15-04-05")
	path := fmt.Sprintf("/tmp/screenshot_%s.png", file_name)
	fmt.Println("saving to ", path)
	f, err := os.Create(path)
	defer f.Close()
	if err != nil {
		fmt.Println(err)
	}

	f.Write(buf.Bytes()) // write png

	SETTINGS.clipboard = buf.Bytes()

}

// This is the core meat for INCR protocol - see SETTINGS for some defaults
// Also notice we are handling different event types here: SelectionRequest and SelectionNotification
// This is how the INCR works for details see:
// https://www.x.org/releases/X11R7.6/doc/xorg-docs/specs/ICCCM/icccm.html#incr_properties
func process_incr(ev interface{}, X *xgb.Conn, wid xproto.Window, txt []byte) {

	targets, err := xproto.InternAtom(X, true, uint16(len("TARGETS")), "TARGETS").Reply()
	nnn := "image/png"
	ttt, err := xproto.InternAtom(X, true, uint16(len(nnn)), nnn).Reply()
	incr, err := xproto.InternAtom(X, true, uint16(len("INCR")), "INCR").Reply()
	_ = err

	if SETTINGS.incr == false {
		e, ok := ev.(xproto.SelectionRequestEvent)
		if !ok {
			return
		}

		win := e.Requestor
		pty := e.Property
		SETTINGS.incr_win = win
		SETTINGS.incr_pty = pty
		SETTINGS.incr_pos = 0

		fmt.Println(" INCR-false -----", "property", e)
		if e.Target == targets.Atom {
			fmt.Println(" targets case -----", e)

			Xutil, _ := xgbutil.NewConnXgb(X)
			name, _ := xprop.AtomName(Xutil, e.Target)
			fmt.Println(name)
			name, _ = xprop.AtomName(Xutil, e.Property)
			fmt.Println(name)

			data := make([]byte, 8)
			xgb.Put32(data, uint32(targets.Atom)) // this is apparently not needed
			xgb.Put32(data[4:8], uint32(ttt.Atom))
			xproto.ChangeProperty(X, xproto.PropModeReplace,
				e.Requestor, pty, xproto.AtomAtom, byte(32), /*format*/
				uint32(2) /*length*/, data)

		} else if len(txt) > SETTINGS.incr_chunk_length {
			// fmt.Println("--else if: INCR start")

			// delete INCR property
			xproto.ChangeProperty(X, xproto.PropModeReplace,
				win, pty, incr.Atom, byte(32), 0, []byte{})
			xproto.ChangeWindowAttributes(X, win, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})

			SETTINGS.incr = true
			fmt.Println("-- INCR first delete")
		} else {

			xproto.ChangeProperty(X, xproto.PropModeReplace,
				e.Requestor /*win*/, pty, ttt.Atom, byte(8),
				uint32(len(txt)), txt)
		}
		res := xproto.SelectionNotifyEvent{
			Property:  pty,
			Requestor: win,
			Selection: e.Selection,
			Target:    e.Target,
			Time:      e.Time,
		}

		xproto.SendEvent(X, false, e.Requestor, 0, string(res.Bytes()))

	}

	if SETTINGS.incr == true {
		e, ok := ev.(xproto.PropertyNotifyEvent)
		_, _ = ev, ok
		if !ok {
			return
		}

		if xproto.PropertyDelete != e.State {
			return
		}

		fmt.Println("deleted property ===== received notification ")

		// Test first for the last round as we just want to singnal completion
		// This step also has to happen in its 'own' Notification event.
		if SETTINGS.incr_last_round == true {
			fmt.Println("last round------")
			xproto.ChangeProperty(X, xproto.PropModeReplace,
				SETTINGS.incr_win, SETTINGS.incr_pty, ttt.Atom, byte(8), 0, []byte{})

			SETTINGS.incr = false
			SETTINGS.incr_pos = 0
			SETTINGS.incr_next_pos = SETTINGS.incr_chunk_length
			SETTINGS.incr_last_round = false
			return
		}

		SETTINGS.incr_next_pos = SETTINGS.incr_pos + SETTINGS.incr_chunk_length
		chunk_len := SETTINGS.incr_chunk_length
		if SETTINGS.incr_next_pos >= len(txt) {
			SETTINGS.incr_next_pos = len(txt)
			chunk_len = SETTINGS.incr_next_pos - SETTINGS.incr_pos
			SETTINGS.incr_last_round = true
		}

		xproto.ChangeProperty(X, xproto.PropModeReplace,
			SETTINGS.incr_win, SETTINGS.incr_pty, ttt.Atom, byte(8),
			uint32(chunk_len), txt[SETTINGS.incr_pos:SETTINGS.incr_next_pos])

		SETTINGS.incr_pos = SETTINGS.incr_next_pos

	}
}

// midRect takes an (x, y) position where the pointer was clicked, along with
// the width and height of the thing being drawn and the width and height of
// the canvas, and returns a Rectangle whose midpoint (roughly) is (x, y) and
// whose width and height match the parameters when the rectangle doesn't
// extend past the border of the canvas. Make sure to check if the rectangle is
// empty or not before using it!
func midRect(x, y, width, height, canWidth, canHeight int) image.Rectangle {
	val := image.Rect(
		max(0, min(canWidth, x-width/2)),   // top left x
		max(0, min(canHeight, y-height/2)), // top left y
		max(0, min(canWidth, x+width/2)),   // bottom right x
		max(0, min(canHeight, y+height/2)), // bottom right y
	)
	// fmt.Println(val)

	return val
}

// This method currenlty just paints the brush stroke.
// Start/End represent Bounding box corners
// TODO: draw real area for better preview
func paint(canvas *xgraphics.Image, win *xwindow.Window, x, y, prev_x, prev_y int) {

	bg := xgraphics.BGRA{0x0, 0x0, 0x0, 0xff}
	_ = bg
	pencil := xgraphics.BGRA{0x00, 0x0, 0xff, 255}
	pencilTip := SETTINGS.brush_size
	width := canvas.Rect.Dx()
	height := canvas.Rect.Dy()

	// Add heuristic to skip every some pixels to speed up painting
	// When generating full range - every pixel painting is too slow
	// make range of a singel element to return to the reugular version
	x_range := makeRange(prev_x, x, 1)
	y_range := makeRange(prev_y, y, 1)

	// Make both ranges equal length - as we want to match corresponding
	// x,y coordinates and paint once
	// fmt.Println("x,y:", x,y, prev_x, prev_y)
	// fmt.Println("   x_range", x_range)
	// fmt.Println("   y_range", y_range)

	// Make sure both inputs are the same length
	x_range, y_range = extendRange(x_range, y_range)

	for i, _ := range x_range {

		x = x_range[i]
		y = y_range[i]

		tipRect := midRect(x, y, pencilTip, pencilTip, width, height)
		_ = tipRect

		// If the rectangle contains no pixels, don't draw anything.
		if tipRect.Empty() {
			return
		}

		// Create the subimage of the canvas to draw to.
		tip := canvas.SubImage(tipRect).(*xgraphics.Image)

		// Now color each pixel in tip with the pencil color.
		tip.For(func(x, y int) xgraphics.BGRA {
			return xgraphics.BlendBGRA(tip.At(x, y).(xgraphics.BGRA), pencil /* color*/)
		})

		// Now draw the changes to the pixmap.
		tip.XDraw()

		// And paint them to the window.
		tip.XPaint(win.Id)
	}
}

func drawRestorePrevious(canvas *xgraphics.Image, win *xwindow.Window) {

	///// back to the previously modified image

	img := SETTINGS.undos[len(SETTINGS.undos)-1]

	for i := 0; i < len(canvas.Pix); i += 4 {
		_i := i / 4
		_x := _i % (canvas.Stride / 4)
		_y := _i / (canvas.Stride / 4)

		b := img[i]
		g := img[i+1]
		r := img[i+2]

		canvas.Set(_x, _y, color.RGBA{r, g, b, 255})

	}
	canvas.XDraw()
	canvas.XPaint(win.Id)
}

func drawRect(canvas *xgraphics.Image, win *xwindow.Window, x, y, start_x, start_y int) {

	// restore previously image
	drawRestorePrevious(canvas, win)

	// Now draw the rectangle

	// fmt.Println(start_x, start_y, x, y)

	// Draw bounds outside selection region
	rectXtop := image.Rect(start_x, start_y, x, start_y-2)
	rectXbottom := image.Rect(start_x, y, x, y+2)
	rectYleft := image.Rect(start_x, start_y, start_x-2, y)
	rectYright := image.Rect(x, start_y, x+2, y)

	bounds_arr := []image.Rectangle{rectXtop, rectXbottom, rectYleft, rectYright}

	for _, rect := range bounds_arr {

		if rect.Empty() {
			continue
		}
		// fmt.Println(canvas, rect)

		pencil := xgraphics.BGRA{0x00, 0xff, 0x0, 125}
		// Create the subimage of the canvas to draw to.
		tip := canvas.SubImage(rect).(*xgraphics.Image)

		// Now color each pixel in tip with the pencil color.
		tip.For(func(x, y int) xgraphics.BGRA {
			return xgraphics.BlendBGRA(tip.At(x, y).(xgraphics.BGRA), pencil /* color*/)
		})

		// Now draw the changes to the pixmap.
		tip.XDraw()

		// And paint them to the window.
		tip.XPaint(win.Id)
	}
}

// Function to allow user input and select area of the screen to capture
// shift+(mouse-down) represents top-left corner
// shift+release (mouse-up) represents bottom-right corner
// Here we specify all the key bindings
func screenShotProgram() (rect image.Rectangle) {

	// XCB - version - determin bounds with User's input
	X, err := xgbutil.NewConn()
	if err != nil {
		log.Fatal(err)
	}
	mousebind.Initialize(X)
	keybind.Initialize(X)

	// Capture the state of the current screen - this workarounds the problem
	// of dealing with opacities which may require the feature to be enabled in compositor
	canvas, _ := xgraphics.NewDrawable(X, xproto.Drawable(X.RootWin()))

	bounds := image.Rect(0, 0, canvas.Rect.Dx(), canvas.Rect.Dy())

	win := canvas.XShowExtra("Select area to capture", true)

	// Once initialized turn to fullscreen (f11) to match coordinates on screen
	ewmh.WmStateReq(canvas.X, win.Id, ewmh.StateToggle, "_NET_WM_STATE_FULLSCREEN")

	// painting

	// before first brush stroke - push original image onto undo stack
	undo_step := make([]byte, len(canvas.Pix))
	copy(undo_step, canvas.Pix)
	SETTINGS.undos = append(SETTINGS.undos, undo_step)

	err = mousebind.ButtonPressFun(
		func(X *xgbutil.XUtil, e xevent.ButtonPressEvent) {
			log.Println("Painting")
		}).Connect(X, win.Id, "1", false, true)

	var prev_rx, prev_ry int
	mousebind.Drag(X, win.Id, win.Id, "1", false,
		func(X *xgbutil.XUtil, rx, ry, ex, ey int) (bool, xproto.Cursor) {
			log.Println("starting", rx, ry)
			bounds.Min.X = rx
			bounds.Min.Y = ry
			prev_rx = rx
			prev_ry = ry
			return true, 0
		},
		func(X *xgbutil.XUtil, rx, ry, ex, ey int) {
			// log.Println("painting", rx, ry)
			paint(canvas, win, rx, ry, prev_rx, prev_ry)
			prev_rx = rx
			prev_ry = ry

		},
		func(X *xgbutil.XUtil, rx, ry, ex, ey int) {
			log.Println("release", rx, ry)
			bounds.Max.X = rx
			bounds.Max.Y = ry
			// push on the undo stack
			undo_step := make([]byte, len(canvas.Pix))
			copy(undo_step, canvas.Pix)
			SETTINGS.undos = append(SETTINGS.undos, undo_step)

		})

	// cropping
	var start_rx, start_ry int
	err = mousebind.ButtonPressFun(
		func(X *xgbutil.XUtil, e xevent.ButtonPressEvent) {
			log.Println("Cropping")
		}).Connect(X, win.Id, "Shift-1", false, true)

	mousebind.Drag(X, win.Id, win.Id, "Shift-1", false,
		func(X *xgbutil.XUtil, rx, ry, ex, ey int) (bool, xproto.Cursor) {
			log.Println("starting", rx, ry)
			bounds.Min.X = rx
			bounds.Min.Y = ry

			start_rx = rx
			start_ry = ry
			return true, 0
		},
		func(X *xgbutil.XUtil, rx, ry, ex, ey int) {
			// log.Println("cropping", rx, ry)
			// TODO: crop function
			drawRect(canvas, win, rx, ry, start_rx, start_ry)

		},
		func(X *xgbutil.XUtil, rx, ry, ex, ey int) {
			log.Println("release", rx, ry)
			// once selection is completed - restore previous image
			drawRestorePrevious(canvas, win)
			copyToClipboard(canvas, image.Rect(start_rx, start_ry, rx, ry))

			selection, err := xproto.InternAtom(X.Conn(), true, uint16(len("CLIPBOARD")), "CLIPBOARD").Reply()
			xproto.SetSelectionOwner(X.Conn(), win.Id, selection.Atom, xproto.Timestamp(0))
			_ = err

			bounds.Max.X = rx
			bounds.Max.Y = ry

		})

	keybind.KeyPressFun(
		func(X *xgbutil.XUtil, e xevent.KeyPressEvent) {
			log.Println("quiting...")
			// graceful exit
			xevent.Detach(win.X, win.Id)
			mousebind.Detach(win.X, win.Id)
			keybind.Detach(win.X, win.Id)
			win.Destroy()
			xevent.Quit(X)

		}).Connect(X, win.Id, "Escape", true)

	keybind.KeyPressFun(
		func(X *xgbutil.XUtil, e xevent.KeyPressEvent) {
			SETTINGS.brush_size += 1
		}).Connect(X, win.Id, "bracketright", true)

	keybind.KeyPressFun(
		func(X *xgbutil.XUtil, e xevent.KeyPressEvent) {
			if SETTINGS.brush_size > 2 { // using 2 as 1px is not visible
				SETTINGS.brush_size -= 1
			}
			fmt.Println(SETTINGS.brush_size)
		}).Connect(X, win.Id, "bracketleft", true)

	keybind.KeyPressFun(
		func(X *xgbutil.XUtil, e xevent.KeyPressEvent) {
			if len(SETTINGS.undos) <= 1 {
				fmt.Println("Nothing to undo")
				return
			}
			fmt.Println(len(SETTINGS.undos))
			fmt.Println(canvas.Stride, canvas.Rect)

			// pop from the undo stack
			SETTINGS.undos = SETTINGS.undos[:len(SETTINGS.undos)-1]
			img := SETTINGS.undos[len(SETTINGS.undos)-1]

			for i := 0; i < len(canvas.Pix); i += 4 {
				_i := i / 4
				_x := _i % (canvas.Stride / 4)
				_y := _i / (canvas.Stride / 4)

				b := img[i]
				g := img[i+1]
				r := img[i+2]

				canvas.Set(_x, _y, color.RGBA{r, g, b, 255})

			}
			canvas.XDraw()
			canvas.XPaint(win.Id)

		}).Connect(X, win.Id, "Control-z", true)

	if err != nil {
		log.Fatal(err)
	}

	// Add HookFun - this is triggered each time before event switch
	// This is closest to the current working Selection implementaiton based on xclip
	xevent.HookFun(func(X *xgbutil.XUtil, e interface{}) bool {
		process_incr(e, X.Conn(), win.Id /*xproto.Window*/, SETTINGS.clipboard)
		return true
	}).Connect(X)

	win.Listen(xproto.EventMaskPropertyChange, xproto.EventMaskStructureNotify)

	// Main Event loop
	xevent.Main(X)

	return bounds
}

func test_brush_gap_filling() {
	x_range := []int{1183, 1182, 1181, 1180, 1179, 1178, 1177, 1176, 1175, 1174, 1173, 1172, 1171, 1170, 1169, 1168, 1167, 1166, 1165, 1164, 1163, 1162, 1161, 1160}
	y_range := []int{829, 828, 827, 826, 825, 824, 823, 822, 821, 820, 819, 818}

	// Special case for single entry - this complicates a bit matching size of each slice
	// x_range := []int{509, 510}
	// y_range := []int{501}

	x_range, y_range = extendRange(x_range, y_range)
	fmt.Println("x_range", x_range)
	fmt.Println("y_range", y_range)
	fmt.Println(len(x_range), len(y_range))
}

func main() {
	// test_brush_gap_filling()
	// test_draw_line()

	screenShotProgram()
	return
}

/*
TODO:
   -h flag with supported shortcuts
   sample new color - with MMB
   eraser
   colors 1,2,3,4 ...
   increase/decrease brush size with button pressed
   some way (pop-up window) with all supported shortcuts
   circular brush size (instead of rectangle)
   redo ctrl-r
*/
