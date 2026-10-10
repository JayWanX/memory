# memory ZCode UserPromptSubmit hook for Windows PowerShell.

# ZCode sends UTF-8 JSON regardless of the Windows console code page.
[Console]::InputEncoding = [Text.Encoding]::UTF8
$null = [Console]::In.ReadToEnd()

[ordered]@{
    hookSpecificOutput = [ordered]@{
        hookEventName = "UserPromptSubmit"
        additionalContext = "[memory] Evaluate: recall needed? After responding, evaluate: remember needed?"
    }
} | ConvertTo-Json -Compress -Depth 4
