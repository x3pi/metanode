all: build

build:
	go clean -cache
	go build -a -gcflags=-l .
	go build -a -gcflags=-l ./cmd/migrate_data
doc:
	cd doc && pdflatex doc.tex

clean:
	rm -f simple_chain

.PHONY: build doc clean 
