
run:
	go run main.go

build:
	CGO_ENABLED=1 go build -o navicli .
