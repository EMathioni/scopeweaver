// scopeweaver-chatgpt is a local, opt-in OAuth bridge. It never starts a task or
// alters the running ScopeWeaver backend or its profiles automatically.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/chatgpt"
	"github.com/Autumn-27/artex/locale"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Every explanatory CLI message is offered in the two repository languages.
func message(lang, en, ko string) string {
	if lang == "ko" {
		return ko
	}
	return en
}

func run(ctx context.Context, args []string) (result error) {
	if len(args) == 0 {
		fmt.Println("usage / 사용법: scopeweaver-chatgpt login|serve|status|models|profile|logout [--lang en|ko]")
		return nil
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Println("usage / 사용법: scopeweaver-chatgpt login|serve|status|models|profile|logout [--lang en|ko]")
		fmt.Println("Command options / 명령 옵션: scopeweaver-chatgpt COMMAND --help")
		return nil
	}
	command := args[0]
	root, err := chatgpt.DefaultDirectory()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("scopeweaver-chatgpt "+command, flag.ContinueOnError)
	dir := f.String("directory", root, "Private credential directory / 비공개 자격 증명 디렉터리")
	account := f.String("account", "default", "Saved account label / 저장된 계정 이름")
	lang := f.String("lang", "en", "Language: en or ko / 언어: en 또는 ko")
	listen := f.String("listen", "127.0.0.1:8789", "Loopback bridge address / 루프백 브리지 주소")
	callback := f.String("callback-listen", "127.0.0.1:1455", "OAuth loopback listener / OAuth 루프백 리스너")
	model := f.String("model", "", "Account model slug from models / models에서 확인한 계정 모델 slug")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || (*lang != "en" && *lang != "ko") {
		return errors.New("invalid arguments / 잘못된 인수")
	}
	defer func() {
		if result != nil {
			result = errors.New(locale.ErrorMessage(locale.Lang(*lang), result))
		}
	}()
	switch command {
	case "login", "serve", "status", "models", "profile", "logout":
	default:
		return errors.New("unknown command / 알 수 없는 명령")
	}
	s, err := chatgpt.OpenStore(*dir)
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := chatgpt.NewClient(s, *account)
	if err != nil {
		return err
	}
	switch command {
	case "login":
		ln, err := chatgpt.ListenLoopback(*callback)
		if err != nil {
			return err
		}
		defer ln.Close()
		redirect := "http://" + ln.Addr().String() + "/auth/callback"
		a, authorizationURL, err := c.BeginLogin(redirect)
		if err != nil {
			return err
		}
		loginCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		done := make(chan error, 1)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'")
			if r.Host != ln.Addr().String() {
				http.Error(w, "Invalid callback / 잘못된 콜백", 400)
				return
			}
			err := a.Finish(loginCtx, redirect+"?"+r.URL.RawQuery)
			if err != nil {
				http.Error(w, "Sign-in failed; return to the terminal / 로그인 실패; 터미널로 돌아가세요", 400)
			} else {
				fmt.Fprintln(w, message(*lang, "Signed in. You can close this tab.", "로그인되었습니다. 이 탭을 닫아도 됩니다."))
			}
			if a.Completed() && !errors.Is(err, chatgpt.ErrAttemptUsed) {
				select {
				case done <- err:
				default:
				}
			}
		})
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, MaxHeaderBytes: 16 << 10}
		go func() {
			if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case done <- err:
				default:
				}
			}
		}()
		defer server.Close()
		fmt.Fprintln(os.Stderr, message(*lang,
			"Continue with ChatGPT: open the following URL in your browser. If the WSL callback cannot connect, paste the complete callback URL from the browser address bar here. Do not share these URLs.",
			"Continue with ChatGPT: 다음 URL을 브라우저에서 여세요. WSL 콜백에 연결할 수 없다면 브라우저 주소 표시줄의 전체 콜백 URL을 여기에 붙여넣으세요. 이 URL을 공유하지 마세요."))
		fmt.Fprintln(os.Stderr, authorizationURL)
		go func() {
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Buffer(make([]byte, 4096), 64<<10)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				err := a.Finish(loginCtx, line)
				if a.Completed() && !errors.Is(err, chatgpt.ErrAttemptUsed) {
					select {
					case done <- err:
					case <-loginCtx.Done():
					}
					return
				}
				fmt.Fprintln(os.Stderr, message(*lang, "Invalid callback URL; try again.", "잘못된 콜백 URL입니다. 다시 시도하세요."))
			}
		}()
		select {
		case err := <-done:
			if err != nil {
				return err
			}
		case <-loginCtx.Done():
			return errors.New("sign-in cancelled or timed out / 로그인 취소 또는 시간 초과")
		}
		email, id, _ := c.Account()
		fmt.Println(message(*lang, "ChatGPT account connected:", "ChatGPT 계정 연결됨:"), email, id)
	case "logout":
		if err := c.Logout(ctx); err != nil {
			return err
		}
		fmt.Println(message(*lang, "ChatGPT session revoked; local tokens cleared.", "ChatGPT 세션이 해지되었고 로컬 토큰이 삭제되었습니다."))
	case "status":
		email, id, connected := c.Account()
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"account": *account, "email": email, "client_id": id, "signed_in": connected})
	case "profile", "models", "serve":
		if _, err := c.Token(ctx); err != nil {
			return err
		}
		key, err := s.BridgeKey()
		if err != nil {
			return err
		}
		bridge, err := chatgpt.NewBridge(c, key)
		if err != nil {
			return err
		}
		if command == "models" {
			return printModels(ctx, bridge, key)
		}
		ln, err := chatgpt.ListenLoopback(*listen)
		if err != nil {
			return err
		}
		if command == "profile" {
			ln.Close()
			if *model == "" {
				return errors.New("select --model from the models command / models 명령에서 --model을 선택하세요")
			}
			// Only the independently generated local key is printed. OAuth tokens
			// never enter ScopeWeaver's database or HTTP request recorder.
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"name": "ChatGPT (" + *account + ")", "format": "openai-responses", "base_url": "http://" + ln.Addr().String() + "/v1", "api_key": key, "model": *model, "streaming": true, "max_tokens": 0})
		}
		defer ln.Close()
		server := &http.Server{Handler: bridge, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
			_ = server.Close()
		}()
		fmt.Println(message(*lang, "ChatGPT bridge listening at", "ChatGPT 브리지 주소:"), "http://"+ln.Addr().String()+"/v1")
		email, id, _ := c.Account()
		fmt.Println(message(*lang, "Active ChatGPT account:", "활성 ChatGPT 계정:"), *account, email, id)
		err = server.Serve(ln)
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

// Reuse the authenticated bridge handler without exposing a temporary listener.
func printModels(ctx context.Context, handler http.Handler, key string) error {
	r, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := &cliResponse{header: make(http.Header)}
	handler.ServeHTTP(w, r)
	if w.status != 200 {
		return fmt.Errorf("ChatGPT models: HTTP %d / ChatGPT 모델: HTTP %d", w.status, w.status)
	}
	return nil
}

type cliResponse struct {
	header http.Header
	status int
}

func (w *cliResponse) Header() http.Header    { return w.header }
func (w *cliResponse) WriteHeader(status int) { w.status = status }
func (w *cliResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.status == 200 {
		return os.Stdout.Write(b)
	}
	return len(b), nil
}
