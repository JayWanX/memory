# ZCode hook integration tests

These opt-in process tests run with `go test ./test/memory/zcode -v -count=1`.
They are outside `make test`; `make test-integration` includes the package.
On Windows, `powershell.exe` 5.1 is required and the suite installs hooks through
the built product's `setup --target zcode --global --yes` command in a temporary
home containing spaces and Chinese characters. Set `MEMORY_TEST_BIN` to reuse
an existing product binary; otherwise the test builds one.

The **Manual Integration** workflow's `zcode-windows` suite runs only this
boundary on a Windows runner. The default `all` selection also runs the existing
Linux integration suite. The process writes UTF-8 JSON bytes to stdin while a
wrapper selects console code page 936 (GBK) or 65001 (UTF-8). The unchanged
`stop-before-142.ps1` fixture comes from commit
`4d312c4ebb42bf9f85442120fad4b9e85d4779e2`; its failures establish the regression
control in the same host. The fixed scripts must preserve installed bytes,
including the Stop script's UTF-8 BOM, honor the host's active guard, recognize
the documented completion tokens, and produce valid hook JSON.

Empty and malformed payloads retain the existing blocking reminder behavior.
These tests do not claim general natural-language evaluation detection.

On other platforms, the shell Stop cases run normally. Set
`MEMORY_TEST_POWERSHELL` to an explicit `pwsh` path to also probe the PowerShell
assets using the production hook writer. PowerShell 7 evidence does not replace
Windows PowerShell 5.1 validation of source encoding and native setup.

Encoding references:

- [Microsoft's PowerShell character encoding guide](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_character_encoding?view=powershell-5.1)
- [Console.InputEncoding](https://learn.microsoft.com/en-us/dotnet/api/system.console.inputencoding?view=netframework-4.8.1)
- [ZCode hook protocol](https://zcode.z.ai/en/docs/hooks)
