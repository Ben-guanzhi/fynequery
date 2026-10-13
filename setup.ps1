# Make a standalone clone of this repo buildable.
#
# fynequery imports two modules that live in the sibling TableProGo repo -
# tableprosql (per-dialect SQL text, ColumnType) and tableproxlsx (xlsx
# rendering) - and reaches them with relative replace directives
# (../tableprosql, ../tableproxlsx). A bare `git clone` of this repo leaves
# nothing for them to resolve against, and `go build` fails with
# "replacement directory does not exist". This clones the parent into the
# position the replaces expect, then tidies and builds.
#
# Set TABLEPRO_PARENT to place the parent somewhere other than ..\TableProGo.
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$parent = if ($env:TABLEPRO_PARENT) {
    $env:TABLEPRO_PARENT
} else {
    Join-Path $PSScriptRoot '..\TableProGo'
}

if (-not (Test-Path (Join-Path $parent 'tableprosql\go.mod')) -or
    -not (Test-Path (Join-Path $parent 'tableproxlsx\go.mod'))) {
    Write-Host "cloning the parent repo into $parent ..."
    git clone https://github.com/Ben-guanzhi/TableProGo.git $parent
}

foreach ($mod in 'tableprosql', 'tableproxlsx') {
    if (-not (Test-Path (Join-Path $parent (Join-Path $mod 'go.mod')))) {
        throw "missing $(Join-Path $parent $mod)\go.mod after cloning"
    }
}

Write-Host 'shared modules in place; tidying and building ...'
go mod tidy
go build -o fynequery.exe .
