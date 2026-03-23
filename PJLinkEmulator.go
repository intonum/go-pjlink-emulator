package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
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
	AVMUTE_VIDEO = 11 // video muted, audio on
	AVMUTE_AUDIO = 21 // audio muted, video on
	AVMUTE_BOTH  = 31 // both muted
	AVMUTE_NONE  = 30 // both unmuted

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

// PJLinkDevice holds the emulated device state.
type PJLinkDevice struct {
	_PJLinkName      string
	_manufacturer    string
	_model           string
	_PJLinkClass     int
	_port            int

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
	log.Println("POWER -> ON request, state:", d._PJLinkPower)
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
	log.Println("POWER -> OFF request, state:", d._PJLinkPower)
}

// updateThermalState transitions WARMING→ON or COOLING→OFF when the duration has elapsed.
func (d *PJLinkDevice) updateThermalState() {
	d.Lock()
	defer d.Unlock()
	switch d._PJLinkPower {
	case POWER_WARMING:
		if time.Now().After(d._deviceThermalAtTime.Add(d._warmingUpDuration)) {
			d._PJLinkPower = POWER_ON
			log.Println("POWER: WARMING -> ON")
		}
	case POWER_COOLING:
		if time.Now().After(d._deviceThermalAtTime.Add(d._coolingDownDuration)) {
			d._PJLinkPower = POWER_OFF
			log.Println("POWER: COOLING -> OFF")
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
		log.Println("INPT: invalid source", source)
		return false
	}
	d._PJLinkInput = source
	log.Println("INPT:", d._PJLinkInput)
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
	log.Println("AVMT:", d._PJLinkAVMute)
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

func replyValue(header string, value string, conn net.Conn) {
	line := header + "=" + value + "\r"
	conn.Write([]byte(line))
	log.Printf("TX: %s", strings.TrimRight(line, "\r"))
}

func replyOK(header string, conn net.Conn) {
	line := header + "=OK\r"
	conn.Write([]byte(line))
	log.Printf("TX: %s", strings.TrimRight(line, "\r"))
}

// replyERR sends %xCMD=ERRn\r  (errCode: 1–4)
func replyERR(header string, errCode int, conn net.Conn) {
	line := fmt.Sprintf("%s=ERR%d\r", header, errCode)
	conn.Write([]byte(line))
	log.Printf("TX: %s", strings.TrimRight(line, "\r"))
}

// --- main ---

func main() {
	rand.Seed(time.Now().UnixNano())
	log.SetOutput(os.Stdout)
	log.Println("Application version:", Version)

	isDisplayPtr  := flag.Bool("display", false, "Emulate a display instead of a projector")
	namePtr       := flag.String("name", "", "Device name (default: random)")
	mfgPtr        := flag.String("manufacturer", "", "Manufacturer name")
	modelPtr      := flag.String("model", "", "Model name")
	lampHoursPtr  := flag.Int("lamp-hours", -1, "Lamp hours for projector (-1 = use default of 10)")
	flag.Parse()

	var device PJLinkDevice
	if *isDisplayPtr {
		fmt.Println("Will emulate a display...")
		device = NewDisplay(*namePtr, *mfgPtr, *modelPtr)
	} else {
		fmt.Println("Will emulate a projector...")
		device = NewProjector(*namePtr, *mfgPtr, *modelPtr, *lampHoursPtr)
	}

	log.Println("Device name       :", device._PJLinkName)
	log.Println("Manufacturer      :", device._manufacturer)
	log.Println("Model             :", device._model)
	log.Println("Class             :", device._PJLinkClass)
	log.Println("Lamp hours        :", device._PJLinkLampHours)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", device._port))
	if err != nil {
		panic(err)
	}
	log.Printf("Listening on TCP :%d", device._port)

	go startUDPServer(device._port, &device)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Accept error:", err)
			continue
		}
		log.Println("Connection from", conn.RemoteAddr())
		// No-auth greeting — exactly "PJLINK 0\r"
		conn.Write([]byte("PJLINK 0\r"))
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

	log.Println("RX:", command)
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

func startUDPServer(port int, device *PJLinkDevice) {
	udpServer, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatal("UDP listen error:", err)
	}
	defer udpServer.Close()
	log.Printf("Listening on UDP :%d", port)

	for {
		buf := make([]byte, 1024)
		_, addr, err := udpServer.ReadFrom(buf)
		if err != nil {
			continue
		}
		go handleUDP(udpServer, addr, buf, device)
	}
}

func handleUDP(udpServer net.PacketConn, addr net.Addr, buf []byte, device *PJLinkDevice) {
	msg := strings.TrimRight(string(buf), "\x00\r\n")
	log.Println("UDP RX:", msg, "from", addr)

	if msg == "%2SRCH" {
		// Respond with a dummy MAC address per PJLink search spec
		resp := "%2ACKN=00:00:00:00:00:00\r"
		udpServer.WriteTo([]byte(resp), addr)
		log.Println("UDP TX:", strings.TrimRight(resp, "\r"))
	}
}
