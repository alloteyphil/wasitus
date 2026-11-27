# Offline timeline demo for the README (no Docker / secrets).
# Regenerate: vhs docs/demo/offline-timeline.tape

Output docs/assets/offline-timeline.gif

Require go

Set Shell zsh
Set FontSize 14
Set Width 980
Set Height 420
Set Padding 12
Set Theme "Catppuccin Mocha"
Set TypingSpeed 35ms
Set Framerate 20
Set PlaybackSpeed 1.0

Type `go run ./cmd/wasitus timeline --config examples/wasitus.example.yaml --from "2026-11-14 03:00" --to "2026-11-14 04:00"`
Enter
Sleep 4s
