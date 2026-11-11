APP_NAME := polka
DUB      := dub
COMPILER ?= ldc2

.PHONY: build run test lint clean

build:
	$(DUB) build --compiler=$(COMPILER)

run:
	$(DUB) run --compiler=$(COMPILER) -- $(ARGS)

test:
	$(DUB) test --compiler=$(COMPILER)

lint:
	$(DUB) build --compiler=$(COMPILER) --build=unittest --force

clean:
	rm -f $(APP_NAME) $(APP_NAME).exe $(APP_NAME)-test-application $(APP_NAME)-test-application.exe coverage.txt *.o *.obj
	rm -rf .dub
