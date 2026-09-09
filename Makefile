# FlacPlayer 构建/测试入口
# 用法:
#   make build         本机编译 -> build/Fplay
#   make run           本机编译并运行(build/Fplay)
#   make test          单元+集成测试
#   make test-race     -race 测试(解码并发)
#   make vet           go vet ./...
#   make fmt           检查格式(gofmt -l)
#   make lint          gofmt + go vet
#   make cross         交叉编译到 build/ 目录:linux/amd64、linux/arm64
#   make install       安装到 ~/.local/bin/Fplay
#   make clean         清掉 build/

BINARY  := Fplay
OUTDIR  := build
GO      ?= go
LDFLAGS := -s -w

.PHONY: build run test test-race vet fmt lint cross install clean

build:
	@mkdir -p $(OUTDIR)
	$(GO) build -ldflags '$(LDFLAGS)' -trimpath -o $(OUTDIR)/$(BINARY) .

run: build
	$(OUTDIR)/$(BINARY)

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	@echo "== gofmt check =="
	@gofmt -l . | grep -v '^$$' ; test -z "$$(gofmt -l .)" || (echo "gofmt 有未格式化文件,请先 gofmt -w ."; exit 1)

lint: fmt vet

# 交叉编译: 目标目录 build/<os>-<arch>/Fplay[.exe]
cross:
	@mkdir -p $(OUTDIR)/linux-amd64 $(OUTDIR)/linux-arm64
	@echo "== linux/amd64 =="
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags '$(LDFLAGS)' -trimpath -o $(OUTDIR)/linux-amd64/$(BINARY) .
	@echo "== linux/arm64 (嵌入式 PCB 主目标) =="
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags '$(LDFLAGS)' -trimpath -o $(OUTDIR)/linux-arm64/$(BINARY) .
	@ls -lh $(OUTDIR)/*/$(BINARY) 2>/dev/null

install: build
	install -m 0755 $(OUTDIR)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "已安装到 $(HOME)/.local/bin/$(BINARY)"

clean:
	rm -rf $(OUTDIR)
