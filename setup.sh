#!/bin/sh
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
# Set TABLEPRO_PARENT to place the parent somewhere other than ../TableProGo.
set -eu

cd "$(dirname "$0")"

parent="${TABLEPRO_PARENT:-../TableProGo}"

if [ ! -f "$parent/tableprosql/go.mod" ] || [ ! -f "$parent/tableproxlsx/go.mod" ]; then
    echo "cloning the parent repo into $parent ..."
    git clone https://github.com/Ben-guanzhi/TableProGo.git "$parent"
fi

for mod in tableprosql tableproxlsx; do
    if [ ! -f "$parent/$mod/go.mod" ]; then
        echo "error: $parent/$mod/go.mod is missing after cloning" >&2
        exit 1
    fi
done

echo "shared modules in place; tidying and building ..."
go mod tidy
go build -o fynequery .
