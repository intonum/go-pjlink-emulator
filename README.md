# go-pjlink-emulator

PJLink device emulator written in Go.

## Implemented features

- No-auth greeting (`PJLINK 0\r`)
- PJLink-formatted TCP responses terminated with `\r`
- Projector mode and display mode
- Configurable device name, manufacturer, model, lamp hours, and PJLink class via CLI flags
- Power state handling with warm-up and cool-down timing
- Class 1 commands:
  - `%1CLSS ?`
  - `%1NAME ?`
  - `%1INF1 ?`
  - `%1INF2 ?`
  - `%1POWR ?`, `%1POWR 0`, `%1POWR 1`
  - `%1LAMP ?`
  - `%1INPT ?`, `%1INPT <source>`
  - `%1AVMT ?`, `%1AVMT 10`, `%1AVMT 11`, `%1AVMT 20`, `%1AVMT 21`, `%1AVMT 30`, `%1AVMT 31`
- Class 2 commands:
  - `%2FREZ ?`, `%2FREZ 0`, `%2FREZ 1`
  - `%2SVOL 0`, `%2SVOL 1`
  - `%2MVOL 0`, `%2MVOL 1`
- Basic UDP `%2SRCH` / `%2ACKN=` discovery stub

## Missing features

- Authentication (`PJLINK 1`)
- Class 1 commands not implemented:
  - `%1ERST ?`
  - `%1INST ?`
  - `%1INFO ?`
- Class 2 commands not implemented:
  - `%2SNUM ?`
  - `%2SVER ?`
  - `%2INNM ?`
  - `%2IRES ?`
  - `%2RRES ?`
  - `%2FILT ?`
  - `%2RLMP ?`
  - `%2RFIL ?`
- Standard-compliant UDP search/status notification behavior beyond the current discovery stub
- Automatic status notification support
- Full PJLink protocol coverage

## Running

```bash
go run PJLinkEmulator.go -name "Projector Emulator 661" -manufacturer "Epson" -model "Test Model" -lamp-hours 10

# display mode
go run PJLinkEmulator.go -display

# force class override
go run PJLinkEmulator.go -class 1
```

## References

Software PJLink Test tool version 2.0.1.0.190508:

- https://pjlink.jbmia.or.jp/english/data_cl2/PJLink_5-2.zip

PJLink Specifications Version 2.10:

- https://pjlink.jbmia.or.jp/english/data_cl2/PJLink_5-1.pdf
