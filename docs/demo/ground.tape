# Re-record from the repo root with hyctl on PATH: vhs docs/demo/ground.tape
Output docs/demo/ground.gif

Set Shell bash
Set FontSize 22
Set Width 1800
Set Height 640
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 35ms
Set PlaybackSpeed 1

# A throwaway home, so the check records nothing on this machine.
Hide
Type "export PS1='$ ' HYDRA_HOME=$(mktemp -d) HOME=$(mktemp -d) && cd docs/demo && clear"
Enter
Show

Type "cat report.txt"
Enter
Sleep 2s

Type "cat summary-bad.txt"
Enter
Sleep 1.5s

Type 'hyctl oracle ground --by-sentence --prompt prompt.txt --candidate summary-bad.txt; echo "exit $?"'
Enter
Sleep 3.5s

Type "clear"
Enter
Type "cat summary-good.txt"
Enter
Sleep 1.5s

Type 'hyctl oracle ground --by-sentence --prompt prompt.txt --candidate summary-good.txt; echo "exit $?"'
Enter
Sleep 4s
