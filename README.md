# A minimal screen capture and annotation tool for X11 windowing systems

![](images/demo.gif)

### Key Features:
- Simple annotation and undo capabilities selected region gets copied to X11 clipboard and a file.
- Large selections use INCR protocol.
- No external system dependencies - communication uses xgb bindings to talk to X.

### Shortcuts overview:
For better ergonomics assig the binary to be started with a key shortcuts ie.: (Super+Shift+S) 
Left mouse button (LMB): drawing
Shift + LMB: Selecting region to export. This copies the files to clipboard and also saves a copy to /tmp. Each selection updates content of the clipboard and creates a new files in /tmp
Esc: Exit program
][: Decrease / Increase brush size
Ctrl+Z: Undo
