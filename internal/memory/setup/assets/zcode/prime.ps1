# memory ZCode SessionStart hook for Windows PowerShell.

# ZCode sends UTF-8 JSON regardless of the Windows console code page.
[Console]::InputEncoding = [Text.Encoding]::UTF8
$null = [Console]::In.ReadToEnd()

function Resolve-Memory {
    if ($env:MEMORY_BIN -and (Test-Path -LiteralPath $env:MEMORY_BIN -PathType Leaf)) {
        return $env:MEMORY_BIN
    }

    $command = Get-Command memory -ErrorAction SilentlyContinue
    if ($null -ne $command) {
        return $command.Source
    }

    $fallback = Join-Path $HOME "go\bin\memory.exe"
    if (Test-Path -LiteralPath $fallback -PathType Leaf) {
        return $fallback
    }

    return $null
}

$dataRoot = if ($env:MEMORY_DATA_DIR) { $env:MEMORY_DATA_DIR } else { Join-Path $HOME ".memory" }
$promptDir = Join-Path $dataRoot "prompt"
$guidePath = Join-Path $promptDir "guide.md"
$fallbackGuidePath = Join-Path (Join-Path $HOME ".memory") "prompt\guide.md"
if (!(Test-Path -LiteralPath $guidePath -PathType Leaf) -and (Test-Path -LiteralPath $fallbackGuidePath -PathType Leaf)) {
    $guidePath = $fallbackGuidePath
}

$context = [System.Collections.Generic.List[string]]::new()
$memory = Resolve-Memory
if ($null -ne $memory) {
    $statusText = (& $memory status 2>$null | Out-String).Trim()
    if ($statusText) {
        try {
            $status = $statusText | ConvertFrom-Json
            $insights = if ($null -ne $status.total_insights) { $status.total_insights } else { 0 }
            $edges = if ($null -ne $status.edge_count) { $status.edge_count } else { 0 }
            $context.Add("[memory] Memory active ($insights insights, $edges edges).")
        } catch {
            $context.Add("[memory] Memory active.")
        }
    } else {
        $context.Add("[memory] Memory active.")
    }
} else {
    $context.Add("[memory] Warning: memory not found in PATH. Set MEMORY_BIN or add memory to PATH.")
}

if (Test-Path -LiteralPath $guidePath -PathType Leaf) {
    $context.Add((Get-Content -LiteralPath $guidePath -Raw))
}

$text = ($context -join "`n").Trim()
if ($text) {
    [ordered]@{
        hookSpecificOutput = [ordered]@{
            hookEventName = "SessionStart"
            additionalContext = $text
        }
    } | ConvertTo-Json -Compress -Depth 4
} else {
    "{}"
}
