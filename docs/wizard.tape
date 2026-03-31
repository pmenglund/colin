# Records a first-run Colin config wizard walkthrough.
# Expects LINEAR_API_KEY and GITHUB_TOKEN to already be exported in the shell
# that runs `go tool vhs wizard.tape`.

Output docs/wizard.gif

Require bash

Set Shell "bash"
Set FontSize 28
Set Width 1600
Set Height 1000
Set Padding 24
Set BorderRadius 12
Set WindowBar Colorful
Set TypingSpeed 35ms

Type "colin --workflow WIZARD.md config"
Enter

Sleep 5s
Enter

Sleep 2s
Type "cli"
Sleep 2s
Enter

Sleep 2s
Tab
Sleep 1500ms
Tab
Sleep 1500ms
Tab
Sleep 1500ms
Tab

Sleep 2s
Enter

Sleep 10s
Enter

Sleep 4s
Enter

Sleep 2s
