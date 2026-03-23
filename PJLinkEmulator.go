package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version should be provided during build:
// $ go build -ldflags "-X main.Version 1.4"
var Version = "No Version Provided"

const (
	POWER_OFF     = 0
	POWER_ON      = 1
	POWER_COOLING = 2
	POWER_WARMING = 3

	// AV mute query-response values (what AVMT ? returns)
	AVMUTE_UNMUTE_VIDEO = 10
	AVMUTE_VIDEO        = 11 // video muted, audio on
	AVMUTE_UNMUTE_AUDIO = 20
	AVMUTE_AUDIO        = 21 // audio muted, video on
	AVMUTE_BOTH         = 31 // both muted
	AVMUTE_NONE         = 30 // both unmuted
	AVMUTE_UNMUTE_BOTH  = 30

	INPUT_RGB_1 = 11
	INPUT_RGB_2 = 12
	INPUT_RGB_3 = 13
	INPUT_RGB_4 = 14
	INPUT_RGB_5 = 15
	INPUT_RGB_6 = 16
	INPUT_RGB_7 = 17
	INPUT_RGB_8 = 18
	INPUT_RGB_9 = 19

	INPUT_VIDEO_1 = 21
	INPUT_VIDEO_2 = 22
	INPUT_VIDEO_3 = 23
	INPUT_VIDEO_4 = 24
	INPUT_VIDEO_5 = 25
	INPUT_VIDEO_6 = 26
	INPUT_VIDEO_7 = 27
	INPUT_VIDEO_8 = 28
	INPUT_VIDEO_9 = 29

	INPUT_DIGITAL_1 = 31 // HDMI 1
	INPUT_DIGITAL_2 = 32 // HDMI 2
	INPUT_DIGITAL_3 = 33 // DisplayPort 1
	INPUT_DIGITAL_4 = 34 // DisplayPort 2
	INPUT_DIGITAL_5 = 35 // HDBaseT
	INPUT_DIGITAL_6 = 36 // SDI
	INPUT_DIGITAL_7 = 37
	INPUT_DIGITAL_8 = 38
	INPUT_DIGITAL_9 = 39

	INPUT_STORAGE_1 = 41
	INPUT_STORAGE_2 = 42
	INPUT_STORAGE_3 = 43
	INPUT_STORAGE_4 = 44
	INPUT_STORAGE_5 = 45
	INPUT_STORAGE_6 = 46
	INPUT_STORAGE_7 = 47
	INPUT_STORAGE_8 = 48
	INPUT_STORAGE_9 = 49

	INPUT_NETWORK_1 = 51
	INPUT_NETWORK_2 = 52
	INPUT_NETWORK_3 = 53
	INPUT_NETWORK_4 = 54
	INPUT_NETWORK_5 = 55
	INPUT_NETWORK_6 = 56
	INPUT_NETWORK_7 = 57
	INPUT_NETWORK_8 = 58
	INPUT_NETWORK_9 = 59
)

const (
	ansiReset          = "\033[0m"
	ansiInfoColor      = "\033[38;5;214m"
	ansiRXColor        = "\033[36m"
	ansiTXColor        = "\033[32m"
	ansiDetailColor    = "\033[33m"
	ansiSetDetailColor = "\033[35m"
)

// PJLinkDevice holds the emulated device state.
type PJLinkDevice struct {
	_PJLinkName   string
	_manufacturer string
	_model        string
	_PJLinkClass  int
	_port         int

	_PJLinkPower     int
	_PJLinkInput     int
	_PJLinkAVMute    int // canonical query value: 11, 21, 31, 30
	_PJLinkLampHours int // -1 means no lamp (display)
	_PJLinkFreeze    int // 0 = off, 1 = frozen

	_coolingDownDuration time.Duration
	_warmingUpDuration   time.Duration
	_deviceThermalAtTime time.Time

	sync.Mutex
}

// --- State mutators ---

func (d *PJLinkDevice) turnPowerOn() {
	d.Lock()
	defer d.Unlock()
	if d._warmingUpDuration == 0 {
		d._PJLinkPower = POWER_ON
	} else {
		d._PJLinkPower = POWER_WARMING
		d._deviceThermalAtTime = time.Now()
	}
}

func (d *PJLinkDevice) turnPowerOff() {
	d.Lock()
	defer d.Unlock()
	// Fix: was incorrectly checking _warmingUpDuration; cooling uses _coolingDownDuration
	if d._coolingDownDuration == 0 {
		d._PJLinkPower = POWER_OFF
	} else {
		d._PJLinkPower = POWER_COOLING
		d._deviceThermalAtTime = time.Now()
	}
}

// updateThermalState transitions WARMING→ON or COOLING→OFF when the duration has elapsed.
func (d *PJLinkDevice) updateThermalState() {
	d.Lock()
	defer d.Unlock()
	switch d._PJLinkPower {
	case POWER_WARMING:
		if time.Now().After(d._deviceThermalAtTime.Add(d._warmingUpDuration)) {
			d._PJLinkPower = POWER_ON
		}
	case POWER_COOLING:
		if time.Now().After(d._deviceThermalAtTime.Add(d._coolingDownDuration)) {
			d._PJLinkPower = POWER_OFF
		}
	}
}

func validInputSource(source int) bool {
	if source < INPUT_RGB_1 || source > INPUT_NETWORK_9 {
		return false
	}

	terminal := source % 10
	category := source / 10

	return terminal >= 1 && terminal <= 9 && category >= 1 && category <= 5
}

// setInput sets the active input source. Returns false if source is out of Class 1 range.
func (d *PJLinkDevice) setInput(source int) bool {
	d.Lock()
	defer d.Unlock()
	if !validInputSource(source) {
		return false
	}
	d._PJLinkInput = source
	return true
}

// setAVMute applies a mute command (11/10/21/20/31/30) and updates canonical query state.
func (d *PJLinkDevice) setAVMute(cmd int) bool {
	d.Lock()
	defer d.Unlock()

	videoMuted := d._PJLinkAVMute == AVMUTE_VIDEO || d._PJLinkAVMute == AVMUTE_BOTH
	audioMuted := d._PJLinkAVMute == AVMUTE_AUDIO || d._PJLinkAVMute == AVMUTE_BOTH

	switch cmd {
	case 11:
		videoMuted = true
	case 10:
		videoMuted = false
	case 21:
		audioMuted = true
	case 20:
		audioMuted = false
	case 31:
		videoMuted, audioMuted = true, true
	case 30:
		videoMuted, audioMuted = false, false
	default:
		return false
	}

	switch {
	case videoMuted && audioMuted:
		d._PJLinkAVMute = AVMUTE_BOTH
	case videoMuted:
		d._PJLinkAVMute = AVMUTE_VIDEO
	case audioMuted:
		d._PJLinkAVMute = AVMUTE_AUDIO
	default:
		d._PJLinkAVMute = AVMUTE_NONE
	}
	return true
}

// --- Constructors ---

func NewProjector(name, manufacturer, model string, lampHours int) PJLinkDevice {
	if name == "" {
		name = fmt.Sprintf("Projector Emulator %d", rand.Intn(998)+1)
	}
	if manufacturer == "" {
		manufacturer = "PJLink Emulator Manufacturer"
	}
	if model == "" {
		model = "PJLink Emulator Model"
	}
	if lampHours < 0 {
		lampHours = 10
	}
	return PJLinkDevice{
		_PJLinkName:          name,
		_manufacturer:        manufacturer,
		_model:               model,
		_PJLinkClass:         2,
		_port:                4352,
		_PJLinkPower:         POWER_OFF,
		_PJLinkInput:         INPUT_DIGITAL_1,
		_PJLinkAVMute:        AVMUTE_NONE,
		_PJLinkLampHours:     lampHours,
		_PJLinkFreeze:        0,
		_coolingDownDuration: 12 * time.Second,
		_warmingUpDuration:   6 * time.Second,
	}
}

func NewDisplay(name, manufacturer, model string) PJLinkDevice {
	if name == "" {
		name = fmt.Sprintf("Display Emulator %d", rand.Intn(998)+1)
	}
	if manufacturer == "" {
		manufacturer = "PJLink Emulator Manufacturer"
	}
	if model == "" {
		model = "PJLink Emulator Model"
	}
	return PJLinkDevice{
		_PJLinkName:      name,
		_manufacturer:    manufacturer,
		_model:           model,
		_PJLinkClass:     1,
		_port:            4352,
		_PJLinkPower:     POWER_OFF,
		_PJLinkInput:     INPUT_DIGITAL_1,
		_PJLinkAVMute:    AVMUTE_NONE,
		_PJLinkLampHours: -1, // no lamp
		_PJLinkFreeze:    0,
	}
}

// --- PJLink response helpers ---
// All responses follow the form:  %<class><CMD>=<param>\r
// The header is extracted from the received command line (e.g. "%1POWR" from "%1POWR ?").

func cmdHeader(command string) string {
	// Header is everything up to the first space.
	if idx := strings.Index(command, " "); idx >= 0 {
		return command[:idx]
	}
	return command
}

func logProtocolLine(label, color, line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}

	detail := describePJLinkLine(line)
	if detail == "" {
		log.Printf("%s: %s%s%s", label, color, line, ansiReset)
		return
	}

	log.Printf("%s: %s%s%s %s(%s)%s", label, color, line, ansiReset, detailColorForLine(label, line), detail, ansiReset)
}

func isQueryLine(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasSuffix(line, " ?") || strings.HasSuffix(line, "?")
}

func detailColorForLine(label, line string) string {
	if label == "RX" && !isQueryLine(line) {
		return ansiSetDetailColor
	}

	return ansiDetailColor
}

func logInfoLine(format string, args ...interface{}) {
	log.Printf("%s%s%s", ansiInfoColor, fmt.Sprintf(format, args...), ansiReset)
}

func logStartupField(label string, value interface{}) {
	logInfoLine("%-18s: %v", label, value)
}

func describePJLinkLine(line string) string {
	switch strings.TrimSpace(line) {
	case "PJLINK 0":
		return "no-auth greeting"
	case "%2SRCH":
		return "device discovery probe"
	case "%2ACKN=00:00:00:00:00:00":
		return "discovery response with dummy MAC"
	}

	if !strings.HasPrefix(line, "%") {
		return ""
	}

	if strings.Contains(line, "=") {
		return describePJLinkResponse(line)
	}

	return describePJLinkCommand(line)
}

func describePJLinkCommand(line string) string {
	header, param, found := strings.Cut(line, " ")
	if !found {
		return ""
	}

	param = strings.TrimSpace(param)

	switch header {
	case "%1CLSS":
		if param == "?" {
			return "query PJLink class"
		}
	case "%1NAME":
		if param == "?" {
			return "query device name"
		}
	case "%1INF1":
		if param == "?" {
			return "query manufacturer"
		}
	case "%1INF2":
		if param == "?" {
			return "query model"
		}
	case "%1POWR":
		switch param {
		case "?":
			return "query power state"
		case "0":
			return "power off requested"
		case "1":
			return "power on requested"
		}
	case "%1LAMP":
		if param == "?" {
			return "query lamp status"
		}
	case "%1INPT":
		if param == "?" {
			return "query active input"
		}

		source, err := strconv.Atoi(param)
		if err == nil {
			return "switch input to " + describeInputSource(source)
		}
	case "%1AVMT":
		switch param {
		case "?":
			return "query A/V mute state"
		case fmt.Sprint(AVMUTE_VIDEO):
			return "video mute requested"
		case fmt.Sprint(AVMUTE_UNMUTE_VIDEO):
			return "video got unmuted"
		case fmt.Sprint(AVMUTE_AUDIO):
			return "audio mute requested"
		case fmt.Sprint(AVMUTE_UNMUTE_AUDIO):
			return "audio got unmuted"
		case fmt.Sprint(AVMUTE_BOTH):
			return "A/V mute requested"
		case fmt.Sprint(AVMUTE_UNMUTE_BOTH):
			return "audio and video got unmuted"
		}
	case "%2FREZ":
		switch param {
		case "?":
			return "query freeze state"
		case "0":
			return "freeze disabled"
		case "1":
			return "freeze enabled"
		}
	case "%2SVOL":
		switch param {
		case "0":
			return "speaker volume down requested"
		case "1":
			return "speaker volume up requested"
		}
	case "%2MVOL":
		switch param {
		case "0":
			return "microphone volume down requested"
		case "1":
			return "microphone volume up requested"
		}
	}

	return ""
}

func describePJLinkResponse(line string) string {
	header, value, found := strings.Cut(line, "=")
	if !found {
		return ""
	}

	value = strings.TrimSpace(value)

	if strings.HasPrefix(value, "ERR") {
		return describePJLinkError(value)
	}

	if value == "OK" {
		return describePJLinkOK(header)
	}

	switch header {
	case "%1CLSS":
		switch value {
		case "1":
			return "Class 1 device"
		case "2":
			return "Class 2 device"
		}
	case "%1NAME":
		return "device name"
	case "%1INF1":
		return "manufacturer"
	case "%1INF2":
		return "model"
	case "%1POWR":
		switch value {
		case "0":
			return "powered off"
		case "1":
			return "powered on"
		case "2":
			return "cooling down"
		case "3":
			return "warming up"
		}
	case "%1LAMP":
		parts := strings.Fields(value)
		if len(parts) == 2 {
			lampState := "lamp off"
			if parts[1] == "1" {
				lampState = "lamp on"
			}
			return fmt.Sprintf("lamp hours %s, %s", parts[0], lampState)
		}
	case "%1INPT":
		source, err := strconv.Atoi(value)
		if err == nil {
			return "active input " + describeInputSource(source)
		}
	case "%1AVMT":
		switch value {
		case fmt.Sprint(AVMUTE_VIDEO):
			return "video muted"
		case fmt.Sprint(AVMUTE_AUDIO):
			return "audio muted"
		case fmt.Sprint(AVMUTE_BOTH):
			return "A/V muted"
		case fmt.Sprint(AVMUTE_NONE):
			return "audio and video unmuted"
		}
	case "%2FREZ":
		switch value {
		case "0":
			return "image live"
		case "1":
			return "image frozen"
		}
	}

	return ""
}

func describePJLinkOK(header string) string {
	switch header {
	case "%1POWR":
		return "power command accepted"
	case "%1INPT":
		return "input switch accepted"
	case "%1AVMT":
		return "A/V mute command accepted"
	case "%2FREZ":
		return "freeze command accepted"
	case "%2SVOL":
		return "speaker volume command accepted"
	case "%2MVOL":
		return "microphone volume command accepted"
	default:
		return "command accepted"
	}
}

func describePJLinkError(errCode string) string {
	switch errCode {
	case "ERR1":
		return "undefined command"
	case "ERR2":
		return "parameter out of range"
	case "ERR3":
		return "command unavailable in current state"
	case "ERR4":
		return "device failure"
	default:
		return ""
	}
}

func describeInputSource(source int) string {
	switch source {
	case INPUT_DIGITAL_1:
		return "DIGITAL 1 (HDMI 1)"
	case INPUT_DIGITAL_2:
		return "DIGITAL 2 (HDMI 2)"
	case INPUT_DIGITAL_3:
		return "DIGITAL 3 (DisplayPort 1)"
	case INPUT_DIGITAL_4:
		return "DIGITAL 4 (DisplayPort 2)"
	case INPUT_DIGITAL_5:
		return "DIGITAL 5 (HDBaseT)"
	case INPUT_DIGITAL_6:
		return "DIGITAL 6 (SDI)"
	}

	inputNames := map[int]string{
		1: "RGB",
		2: "VIDEO",
		3: "DIGITAL",
		4: "STORAGE",
		5: "NETWORK",
	}

	category := source / 10
	terminal := source % 10
	name, ok := inputNames[category]
	if !ok || terminal < 1 || terminal > 9 {
		return fmt.Sprintf("input %d", source)
	}

	return fmt.Sprintf("%s %d", name, terminal)
}

func replyValue(header string, value string, conn net.Conn) {
	line := header + "=" + value + "\r"
	conn.Write([]byte(line))
	logProtocolLine("TX", ansiTXColor, line)
}

func replyOK(header string, conn net.Conn) {
	line := header + "=OK\r"
	conn.Write([]byte(line))
	logProtocolLine("TX", ansiTXColor, line)
}

// replyERR sends %xCMD=ERRn\r  (errCode: 1–4)
func replyERR(header string, errCode int, conn net.Conn) {
	line := fmt.Sprintf("%s=ERR%d\r", header, errCode)
	conn.Write([]byte(line))
	logProtocolLine("TX", ansiTXColor, line)
}

// --- main ---

func main() {
	rand.Seed(time.Now().UnixNano())
	log.SetOutput(os.Stdout)

	isDisplayPtr := flag.Bool("display", false, "Emulate a display instead of a projector")
	classPtr := flag.Int("class", 0, "Force PJLink class (1 or 2, default: auto based on projector/display mode)")
	namePtr := flag.String("name", "", "Device name (default: random)")
	mfgPtr := flag.String("manufacturer", "", "Manufacturer name")
	modelPtr := flag.String("model", "", "Model name")
	lampHoursPtr := flag.Int("lamp-hours", -1, "Lamp hours for projector (-1 = use default of 10)")
	flag.Parse()

	var device PJLinkDevice
	if *isDisplayPtr {
		device = NewDisplay(*namePtr, *mfgPtr, *modelPtr)
	} else {
		device = NewProjector(*namePtr, *mfgPtr, *modelPtr, *lampHoursPtr)
	}

	if *classPtr != 0 {
		if *classPtr != 1 && *classPtr != 2 {
			log.Fatal("invalid -class value: must be 1 or 2")
		}
		device._PJLinkClass = *classPtr
	}

	logStartupField("Device name", device._PJLinkName)
	logStartupField("Manufacturer", device._manufacturer)
	logStartupField("Model", device._model)
	logStartupField("Class", device._PJLinkClass)
	logStartupField("Lamp hours", device._PJLinkLampHours)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", device._port))
	if err != nil {
		panic(err)
	}

	udpServer, err := net.ListenPacket("udp", fmt.Sprintf(":%d", device._port))
	if err != nil {
		listener.Close()
		panic(err)
	}

	logStartupField("Listening on TCP", device._port)
	logStartupField("Listening on UDP", device._port)

	interruptCh := make(chan os.Signal, 1)
	signal.Notify(interruptCh, os.Interrupt)
	defer signal.Stop(interruptCh)

	go func() {
		<-interruptCh
		logInfoLine("Shutdown requested")
		listener.Close()
		udpServer.Close()
	}()

	go startUDPServer(udpServer, &device)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				logInfoLine("Shutdown complete")
				return
			}
			log.Println("Accept error:", err)
			continue
		}
		log.Println("Connection from", conn.RemoteAddr())
		// No-auth greeting — exactly "PJLINK 0\r"
		conn.Write([]byte("PJLINK 0\r"))
		logProtocolLine("TX", ansiTXColor, "PJLINK 0\r")
		go handleConnection(conn, &device)
	}
}

// --- TCP connection handler ---

func handleConnection(conn net.Conn, device *PJLinkDevice) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		data, err := reader.ReadString('\r')
		if err != nil {
			return
		}
		if data == "" {
			return
		}
		handleCommand(data, conn, device)
	}
}

func handleCommand(inp string, conn net.Conn, device *PJLinkDevice) {
	// Strip CR (and any stray LF)
	command := strings.TrimRight(inp, "\r\n")
	command = strings.TrimSpace(command)

	if len(command) == 0 {
		return
	}

	if command[0] != '%' {
		log.Println("RX (ignored, not PJLink):", command)
		return
	}

	logProtocolLine("RX", ansiRXColor, command)
	header := cmdHeader(command)

	switch command {

	// ── Class 1 queries ──────────────────────────────────────────────

	case "%1CLSS ?":
		replyValue(header, fmt.Sprint(device._PJLinkClass), conn)

	case "%1NAME ?":
		replyValue(header, device._PJLinkName, conn)

	case "%1INF1 ?":
		replyValue(header, device._manufacturer, conn)

	case "%1INF2 ?":
		replyValue(header, device._model, conn)

	// ── Power ────────────────────────────────────────────────────────

	case "%1POWR ?":
		device.updateThermalState()
		replyValue(header, fmt.Sprint(device._PJLinkPower), conn)

	case "%1POWR 1":
		device.turnPowerOn()
		replyOK(header, conn)

	case "%1POWR 0":
		device.turnPowerOff()
		replyOK(header, conn)

	// ── Lamp ─────────────────────────────────────────────────────────

	case "%1LAMP ?":
		if device._PJLinkLampHours < 0 {
			// No lamp installed → ERR1 per PJLink spec
			replyERR(header, 1, conn)
		} else {
			// Static hours + current on/off state
			onoff := 0
			if device._PJLinkPower == POWER_ON || device._PJLinkPower == POWER_WARMING {
				onoff = 1
			}
			replyValue(header, fmt.Sprintf("%d %d", device._PJLinkLampHours, onoff), conn)
		}

	// ── Input ────────────────────────────────────────────────────────

	case "%1INPT ?":
		replyValue(header, fmt.Sprint(device._PJLinkInput), conn)

	// ── AV Mute ──────────────────────────────────────────────────────

	case "%1AVMT ?":
		replyValue(header, fmt.Sprint(device._PJLinkAVMute), conn)

	case "%1AVMT 11", "%1AVMT 10",
		"%1AVMT 21", "%1AVMT 20",
		"%1AVMT 31", "%1AVMT 30":
		param := strings.TrimPrefix(command, "%1AVMT ")
		cmd, _ := strconv.Atoi(param)
		if !device.setAVMute(cmd) {
			replyERR(header, 2, conn)
		} else {
			replyOK(header, conn)
		}

	// ── Class 2: Freeze ──────────────────────────────────────────────

	case "%2FREZ ?":
		if device._PJLinkClass < 2 {
			replyERR(header, 1, conn) // ERR1 = undefined/unsupported
		} else {
			replyValue(header, fmt.Sprint(device._PJLinkFreeze), conn)
		}

	case "%2FREZ 1":
		if device._PJLinkClass < 2 {
			replyERR(header, 1, conn)
		} else {
			device.Lock()
			device._PJLinkFreeze = 1
			device.Unlock()
			replyOK(header, conn)
		}

	case "%2FREZ 0":
		if device._PJLinkClass < 2 {
			replyERR(header, 1, conn)
		} else {
			device.Lock()
			device._PJLinkFreeze = 0
			device.Unlock()
			replyOK(header, conn)
		}

	// ── Class 2: Speaker / Microphone volume ─────────────────────────
	// Minimal implementation: acknowledge but do not model actual levels.

	case "%2SVOL 1", "%2SVOL 0":
		if device._PJLinkClass < 2 {
			replyERR(header, 1, conn)
		} else {
			replyOK(header, conn)
		}

	case "%2MVOL 1", "%2MVOL 0":
		if device._PJLinkClass < 2 {
			replyERR(header, 1, conn)
		} else {
			replyOK(header, conn)
		}

	// ── Default ──────────────────────────────────────────────────────

	default:
		// Dynamic input-switch command: %1INPT <11..59>
		if strings.HasPrefix(command, "%1INPT ") {
			param := strings.TrimPrefix(command, "%1INPT ")
			source, err := strconv.Atoi(param)
			if err != nil || !validInputSource(source) {
				replyERR(header, 2, conn) // ERR2 = out of parameter
				return
			}
			device.setInput(source)
			replyOK(header, conn)
			return
		}

		// All other unrecognised %x… commands → ERR1 (undefined command)
		replyERR(header, 1, conn)
	}
}

// --- UDP server (PJLink search protocol) ---

func startUDPServer(udpServer net.PacketConn, device *PJLinkDevice) {
	for {
		buf := make([]byte, 1024)
		_, addr, err := udpServer.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go handleUDP(udpServer, addr, buf, device)
	}
}

func handleUDP(udpServer net.PacketConn, addr net.Addr, buf []byte, device *PJLinkDevice) {
	msg := strings.TrimRight(string(buf), "\x00\r\n")
	logProtocolLine("UDP RX", ansiRXColor, msg)
	log.Println("UDP remote:", addr)

	if msg == "%2SRCH" {
		// Respond with a dummy MAC address per PJLink search spec
		resp := "%2ACKN=00:00:00:00:00:00\r"
		udpServer.WriteTo([]byte(resp), addr)
		logProtocolLine("UDP TX", ansiTXColor, resp)
	}
}
