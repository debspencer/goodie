
all: default_css.go
	go build

test:
	go test

cover:
	go test -coverprofile cover.out

show:
	go tool cover -html=cover.out

default_css.go: default.css
	$(MAKE) css

css:
	( echo package $$(basename $$(pwd)) ; echo 'var default_css = `' ; cat default.css ; echo '`' ) > default_css.go

