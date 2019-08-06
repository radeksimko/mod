#!/bin/bash
set -e

if [ ! -d $GOPATH/src/github.com/golang/go ]; then
	git clone git@github.com:golang/go $GOPATH/src/github.com/golang/go
else
	git -C $GOPATH/src/github.com/golang/go pull
fi

cp -r $GOPATH/src/github.com/golang/go/src/cmd/go/internal/modfile modfile
find ./modfile -name '*.go' | xargs -I{} sed -i -e "s:\"cmd/go/internal:\"github.com/radeksimko/mod:" {}

cp -r $GOPATH/src/github.com/golang/go/src/internal/lazyregexp lazyregexp
find ./{lazyregexp,modfile} -name '*.go' | xargs -I{} sed -i -e "s:\"internal/lazyregexp:\"github.com/radeksimko/mod/lazyregexp:" {}
