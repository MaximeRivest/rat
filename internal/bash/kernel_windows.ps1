$ErrorActionPreference = 'Stop'

$script:runspace = [runspacefactory]::CreateRunspace()
$script:runspace.Open()
$script:inputEvent = New-Object System.Threading.AutoResetEvent($false)
$script:inputLock = New-Object object
$script:waitBox = [pscustomobject]@{ Value = $false }
$script:inputBox = [pscustomobject]@{ Value = '' }
$script:executing = $false

function Send($obj) {
    $json = $obj | ConvertTo-Json -Compress -Depth 8
    [Console]::Out.WriteLine($json)
    [Console]::Out.Flush()
}

function Invoke-InRunspace($code) {
    $ps = [powershell]::Create()
    $ps.Runspace = $script:runspace
    try {
        [void]$ps.AddScript($code)
        $result = $ps.Invoke()
        return [pscustomobject]@{ Ps = $ps; Result = $result; Exception = $null }
    } catch {
        return [pscustomobject]@{ Ps = $ps; Result = @(); Exception = $_ }
    }
}

function Convert-ResultToText($items) {
    if ($null -eq $items) {
        return ''
    }
    $lines = New-Object System.Collections.Generic.List[string]
    foreach ($item in $items) {
        if ($null -eq $item) {
            continue
        }
        $text = $item | Out-String -Width 200
        if ($text) {
            $text = $text.TrimEnd("`r", "`n")
            if ($text -ne '') {
                $lines.Add($text)
            }
        }
    }
    return ($lines -join "`n")
}

$init = @'
$global:RATInputEvent = $ExecutionContext.SessionState.PSVariable.GetValue('RATInputEvent')
$global:RATInputLock  = $ExecutionContext.SessionState.PSVariable.GetValue('RATInputLock')
$global:RATInputBox   = $ExecutionContext.SessionState.PSVariable.GetValue('RATInputBox')
$global:RATWaitBox    = $ExecutionContext.SessionState.PSVariable.GetValue('RATWaitBox')
$global:RATSend       = $ExecutionContext.SessionState.PSVariable.GetValue('RATSend')
function global:Read-Host {
    param([string]$Prompt)
    if ($Prompt) {
        & $global:RATSend @{ op = 'input_request'; prompt = $Prompt }
    } else {
        & $global:RATSend @{ op = 'input_request' }
    }
    [Threading.Monitor]::Enter($global:RATInputLock)
    $global:RATWaitBox.Value = $true
    try {
        $null = $global:RATInputEvent.WaitOne()
        $text = [string]$global:RATInputBox.Value
        $global:RATInputBox.Value = ''
    } finally {
        $global:RATWaitBox.Value = $false
        [Threading.Monitor]::Exit($global:RATInputLock)
    }
    return $text.TrimEnd("`r", "`n")
}
'@

$sendFunc = ${function:Send}
$script:runspace.SessionStateProxy.SetVariable('RATInputEvent', $script:inputEvent)
$script:runspace.SessionStateProxy.SetVariable('RATInputLock', $script:inputLock)
$script:runspace.SessionStateProxy.SetVariable('RATInputBox', $script:inputBox)
$script:runspace.SessionStateProxy.SetVariable('RATWaitBox', $script:waitBox)
$script:runspace.SessionStateProxy.SetVariable('RATSend', $sendFunc)
$null = Invoke-InRunspace $init

$visibleVarsFilter = @'
Get-Variable | Where-Object {
    $_.Name -notmatch '^(args|ConfirmPreference|DebugPreference|Error|ErrorActionPreference|ErrorView|ExecutionContext|false|FormatEnumerationLimit|HOME|Host|InformationPreference|input|IsCoreCLR|IsLinux|IsMacOS|IsWindows|LASTEXITCODE|Matches|MyInvocation|NestedPromptLevel|NoProxy|null|OutputEncoding|PID|PROFILE|ProgressPreference|PS.*|PWD|ShellId|StackTrace|true|VerbosePreference|WarningPreference|WhatIfPreference)$' -and
    -not $_.Name.StartsWith('_')
}
'@

while (($line = [Console]::In.ReadLine()) -ne $null) {
    if ([string]::IsNullOrWhiteSpace($line)) {
        continue
    }

    try {
        $req = $line | ConvertFrom-Json
    } catch {
        Send @{ success = $false; error = "invalid json: $($_.Exception.Message)" }
        continue
    }

    switch ($req.op) {
        'ping' {
            Send @{ ok = $true }
        }
        'shutdown' {
            Send @{ ok = $true }
            break
        }
        'input' {
            [Threading.Monitor]::Enter($script:inputLock)
            try {
                $script:inputBox.Value = [string]$req.text
                $script:waitBox.Value = $false
                $script:inputEvent.Set() | Out-Null
            } finally {
                [Threading.Monitor]::Exit($script:inputLock)
            }
            Send @{ ok = $true }
        }
        'status' {
            if ($script:waitBox.Value) {
                Send @{ text = 'waiting_for_input' }
            } elseif ($script:executing) {
                Send @{ text = 'busy' }
            } else {
                Send @{ text = 'idle' }
            }
        }
        'run' {
            $script:executing = $true
            $script:waitBox.Value = $false
            try {
                $inv = Invoke-InRunspace ([string]$req.code)
                $ps = $inv.Ps
                $output = Convert-ResultToText $inv.Result
                $errors = @()
                foreach ($err in $ps.Streams.Error) {
                    $errors += ($err.ToString())
                }
                if ($inv.Exception) {
                    $errors += $inv.Exception.Exception.Message
                }
                $errText = ($errors | Where-Object { $_ -and $_.Trim() -ne '' }) -join "`n"
                $varsInv = Invoke-InRunspace ($visibleVarsFilter + " | Measure-Object | Select-Object -ExpandProperty Count")
                $varCount = 0
                if ($varsInv.Result.Count -gt 0) {
                    [int]::TryParse(($varsInv.Result[0] | Out-String).Trim(), [ref]$varCount) | Out-Null
                }
                Send @{ success = [string]::IsNullOrEmpty($errText); output = $output; error = $errText; vars = $varCount }
            } finally {
                $script:executing = $false
                $script:waitBox.Value = $false
            }
        }
        'look_overview' {
            $code = @"
`$vars = $visibleVarsFilter | Sort-Object Name
if (-not `$vars) { 'powershell idle | 0 vars'; return }
`$lines = @("powershell idle | `$(`$vars.Count) vars", '')
foreach (`$v in `$vars | Select-Object -First 200) {
    `$preview = ''
    if (`$null -ne `$v.Value) {
        `$preview = (`$v.Value | Out-String -Width 120).Trim()
    }
    if ([string]::IsNullOrWhiteSpace(`$preview)) { `$preview = '<null>' }
    if (`$preview.Length -gt 80) { `$preview = `$preview.Substring(0, 77) + '...' }
    `$kind = if (`$null -eq `$v.Value) { 'null' } else { `$v.Value.GetType().Name }
    `$lines += ('{0,-20}  {1,-16}  {2}' -f `$v.Name, `$kind, `$preview.Replace("`r", ' ').Replace("`n", ' '))
}
`$lines -join "`n"
"@
            $inv = Invoke-InRunspace $code
            Send @{ text = (Convert-ResultToText $inv.Result) }
        }
        'look_at' {
            $expr = [string]$req.at
            $escapedExpr = $expr.Replace("'", "''")
            $code = @"
try {
    if (Get-Variable -Name '$escapedExpr' -ErrorAction SilentlyContinue) {
        `$v = Get-Variable -Name '$escapedExpr' -ValueOnly
    } else {
        `$v = Invoke-Expression '$escapedExpr'
    }
    if (`$null -eq `$v) {
        "${expr}: null"
    } else {
        `$kind = `$v.GetType().FullName
        `$text = (`$v | Format-List * | Out-String -Width 160).Trim()
        if ([string]::IsNullOrWhiteSpace(`$text)) {
            `$text = (`$v | Out-String -Width 160).Trim()
        }
        "${expr}: `$kind`n`n`$text"
    }
} catch {
    "${expr}: not found"
}
"@
            $inv = Invoke-InRunspace $code
            Send @{ text = (Convert-ResultToText $inv.Result) }
        }
        'complete' {
            $scriptText = [string]$req.code
            $cursor = [int]$req.cursor
            $escaped = $scriptText.Replace("'", "''")
            $code = @"
`$res = TabExpansion2 -inputScript '$escaped' -cursorColumn $cursor
`$res.CompletionMatches | Select-Object -First 50 | ForEach-Object {
    `$kind = if (`$_.ResultType) { `$_.ResultType.ToString().ToLower() } else { 'value' }
    "{0}  {1}" -f `$_.CompletionText, `$kind
}
"@
            $inv = Invoke-InRunspace $code
            $text = (Convert-ResultToText $inv.Result)
            if ([string]::IsNullOrWhiteSpace($text)) {
                $text = 'No completions.'
            }
            Send @{ text = $text }
        }
        default {
            Send @{ success = $false; error = "unknown op '$($req.op)'" }
        }
    }
}

$script:runspace.Close()
$script:runspace.Dispose()
