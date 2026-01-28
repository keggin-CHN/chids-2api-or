package register

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"orchids-api/internal/tempmail"
)

const (
	orchidsURL      = "https://www.orchids.app/"
	defaultPassword = "OrchidsAuto@2024!"
)

// RegisterService 注册服务
type RegisterService struct {
	tempMail *tempmail.MailTM
	headless bool
}

// RegisterResult 注册结果
type RegisterResult struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	ClientCookie string `json:"client_cookie"`
	SessionID    string `json:"session_id"`
	UserID       string `json:"user_id"`
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
}

// RegisterJSON 用于 API 请求的 JSON 请求体
type RegisterJSON struct {
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
	Code     string `json:"code,omitempty"`
	SignUpID string `json:"sign_up_id,omitempty"`
	Headless bool   `json:"headless,omitempty"`
	Count    int    `json:"count,omitempty"`    // 批量注册数量
	Workers  int    `json:"workers,omitempty"`  // 并发线程数
}

// BatchRegisterResult 批量注册结果
type BatchRegisterResult struct {
	Total     int               `json:"total"`
	Success   int               `json:"success"`
	Failed    int               `json:"failed"`
	Results   []*RegisterResult `json:"results"`
	StartTime time.Time         `json:"start_time"`
	EndTime   time.Time         `json:"end_time"`
	Duration  string            `json:"duration"`
}

// New 创建注册服务
func New() *RegisterService {
	return &RegisterService{
		tempMail: tempmail.New1SecMail(),
		headless: false, // 默认有头模式，方便调试
	}
}

// Register 执行完整的注册流程（使用临时邮箱）
func (r *RegisterService) Register() (*RegisterResult, error) {
	return r.RegisterWithOptions(false)
}

// RegisterWithOptions 带选项的注册
func (r *RegisterService) RegisterWithOptions(headless bool) (*RegisterResult, error) {
	result := &RegisterResult{}

	// 1. 生成临时邮箱
	email, err := r.tempMail.GenerateEmail()
	if err != nil {
		return nil, fmt.Errorf("生成临时邮箱失败: %w", err)
	}
	result.Email = email
	result.Password = defaultPassword
	log.Printf("[自动注册] 生成临时邮箱: %s", email)

	// 2. 使用浏览器自动化完成注册
	clientCookie, err := r.browserRegister(email, defaultPassword, headless)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}

	result.ClientCookie = clientCookie
	result.Success = true
	log.Printf("[自动注册] 注册成功! Email: %s", email)

	return result, nil
}

// browserRegister 使用 chromedp 进行浏览器自动化注册
func (r *RegisterService) browserRegister(email, password string, headless bool) (string, error) {
	// 创建浏览器上下文
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-gpu", false),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("window-size", "1280,800"),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("exclude-switches", "enable-automation"),
		chromedp.Flag("disable-infobars", true),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
	)

	// 自动查找浏览器路径 (优先 Edge)
	if execPath := findBrowserPath(); execPath != "" {
		opts = append(opts, chromedp.ExecPath(execPath))
		log.Printf("[自动注册] 使用浏览器: %s", execPath)
	}

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancel()

	// 设置超时
	ctx, cancel = context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var clientCookie string

	// 1. 访问首页前，先移除自动化检测标志
	log.Printf("[自动注册] 正在打开 Orchids 首页...")
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			// 移除 webdriver 标志，绕过自动化检测
			return chromedp.Evaluate(`
				Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
				window.chrome = {runtime: {}};
				Object.defineProperty(navigator, 'languages', {get: () => ['zh-CN', 'zh', 'en']});
				Object.defineProperty(navigator, 'plugins', {get: () => [1,2,3,4,5]});
			`, nil).Do(ctx)
		}),
		chromedp.Navigate(orchidsURL),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("打开首页失败: %w", err)
	}
	log.Printf("[自动注册] 已打开 Orchids 首页")

	// 2. 点击 Sign in 按钮 (使用精确的 component 属性选择器)
	log.Printf("[自动注册] 正在点击 Sign in...")
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`button[component="SignInButton"]`, chromedp.ByQuery),
		chromedp.Click(`button[component="SignInButton"]`, chromedp.ByQuery),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("点击 Sign in 失败: %w", err)
	}
	log.Printf("[自动注册] 已点击 Sign in")

	// 调试：打印当前页面关键元素
	var debugHTML string
	chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		chromedp.OuterHTML("body", &debugHTML, chromedp.ByQuery).Do(ctx)
		if len(debugHTML) > 500 {
			log.Printf("[自动注册] 页面内容片段: %s...", debugHTML[:500])
		}
		return nil
	}))

	// 3. 等待 Clerk 弹窗加载，点击 Sign up 链接
	log.Printf("[自动注册] 正在等待 Clerk 弹窗并点击 Sign up...")
	err = chromedp.Run(ctx,
		// 等待 Clerk 弹窗内容出现
		chromedp.WaitVisible(`//a[contains(text(),'Sign up')]`, chromedp.BySearch),
		chromedp.Sleep(1*time.Second),
		chromedp.Click(`//a[contains(text(),'Sign up')]`, chromedp.BySearch),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("点击 Sign up 失败: %w", err)
	}
	log.Printf("[自动注册] 已点击 Sign up")

	// 4. 输入邮箱 (Clerk 注册表单)
	log.Printf("[自动注册] 正在输入邮箱...")
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`input[name="emailAddress"]`, chromedp.ByQuery),
		chromedp.Click(`input[name="emailAddress"]`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.SendKeys(`input[name="emailAddress"]`, email, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	)
	if err != nil {
		return "", fmt.Errorf("输入邮箱失败: %w", err)
	}
	log.Printf("[自动注册] 已输入邮箱: %s", email)

	// 5. 输入密码
	log.Printf("[自动注册] 正在输入密码...")
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`input[name="password"]`, chromedp.ByQuery),
		chromedp.Click(`input[name="password"]`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.SendKeys(`input[name="password"]`, password, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	)
	if err != nil {
		return "", fmt.Errorf("输入密码失败: %w", err)
	}
	log.Printf("[自动注册] 已输入密码")

	// 6. 点击 Continue 按钮（排除 "Continue with Google"）
	log.Printf("[自动注册] 正在点击 Continue...")
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`
			(function() {
				var buttons = document.querySelectorAll('button');
				for (var i = 0; i < buttons.length; i++) {
					var text = buttons[i].textContent.trim();
					if (text.match(/^Continue/) && !text.includes('Google')) {
						buttons[i].click();
						return true;
					}
				}
				return false;
			})()
		`, nil),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("点击 Continue 失败: %w", err)
	}
	log.Printf("[自动注册] 已点击 Continue")

	// 7. 等待页面跳转到验证码输入，同时检测并处理 Turnstile
	log.Printf("[自动注册] 等待跳转到验证码页面...")
	for i := 0; i < 30; i++ {
		// 检查是否已到验证码页面
		var onCodePage bool
		chromedp.Run(ctx, chromedp.Evaluate(`
			document.body.innerText.includes('Verify your email') ||
			document.body.innerText.includes('verification code') ||
			!!document.querySelector('input[data-input-otp="true"]')
		`, &onCodePage))
		if onCodePage {
			log.Printf("[自动注册] 已到验证码页面")
			break
		}

		// 检测并点击 Turnstile（使用 JS 查找 iframe）
		var hasTurnstile bool
		chromedp.Run(ctx, chromedp.Evaluate(`
			(function() {
				var iframes = document.querySelectorAll('iframe');
				for (var i = 0; i < iframes.length; i++) {
					if (iframes[i].src && iframes[i].src.includes('challenges.cloudflare.com')) {
						iframes[i].click();
						return true;
					}
				}
				// 也尝试点击 Turnstile 容器
				var container = document.querySelector('[class*="turnstile"]');
				if (container) {
					container.click();
					return true;
				}
				return false;
			})()
		`, &hasTurnstile))
		if hasTurnstile {
			log.Printf("[自动注册] 发现并点击了 Turnstile")
		}

		chromedp.Run(ctx, chromedp.Sleep(1*time.Second))
	}

	// 8. 获取验证码邮件
	log.Printf("[自动注册] 等待验证码邮件...")
	code, err := r.tempMail.WaitForVerificationCode(email, 120*time.Second)
	if err != nil {
		return "", fmt.Errorf("获取验证码失败: %w", err)
	}
	log.Printf("[自动注册] 获取到验证码: %s", code)

	// 9. 输入验证码（单个 OTP input，maxlength=6）
	log.Printf("[自动注册] 正在输入验证码...")
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`input[data-input-otp="true"]`, chromedp.ByQuery),
		chromedp.Click(`input[data-input-otp="true"]`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.SendKeys(`input[data-input-otp="true"]`, code, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("输入验证码失败: %w", err)
	}
	log.Printf("[自动注册] 已输入验证码")

	// 10. 点击 Continue 完成验证
	log.Printf("[自动注册] 点击 Continue 完成验证...")
	chromedp.Run(ctx,
		chromedp.Evaluate(`
			(function() {
				var buttons = document.querySelectorAll('button');
				for (var i = 0; i < buttons.length; i++) {
					var text = buttons[i].textContent.trim();
					if (text.match(/^Continue/) && !text.includes('Google')) {
						buttons[i].click();
						return true;
					}
				}
				return false;
			})()
		`, nil),
		chromedp.Sleep(5*time.Second),
	)

	// 11. 等待注册完成
	log.Printf("[自动注册] 等待注册完成...")
	err = chromedp.Run(ctx,
		chromedp.Sleep(5*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("等待注册完成失败: %w", err)
	}

	// 10. 获取 __client cookie (从 clerk 域名获取)
	log.Printf("[自动注册] 正在获取 cookie...")
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			// 获取包含 clerk 域名的 cookies
			cookies, err := network.GetCookies().
				WithURLs([]string{
					"https://clerk.orchids.app",
					"https://www.orchids.app",
					"https://orchids.app",
				}).Do(ctx)
			if err != nil {
				return err
			}

			for _, cookie := range cookies {
				if cookie.Name == "__client" {
					clientCookie = cookie.Value
					log.Printf("[自动注册] 获取到 __client cookie (域名: %s, 长度: %d)", cookie.Domain, len(clientCookie))
					return nil
				}
			}

			// 如果没找到，打印所有 cookies 以便调试
			log.Printf("[自动注册] 未找到 __client cookie，当前所有 cookies:")
			for _, cookie := range cookies {
				log.Printf("  - %s (域名: %s): %s...", cookie.Name, cookie.Domain, truncate(cookie.Value, 50))
			}

			return nil
		}),
	)
	if err != nil {
		return "", fmt.Errorf("获取 cookies 失败: %w", err)
	}

	if clientCookie == "" {
		return "", fmt.Errorf("未能获取 __client cookie，请检查浏览器窗口")
	}

	return clientCookie, nil
}

// RegisterWithCustomEmail 使用自定义邮箱注册（需要手动提供验证码）
func (r *RegisterService) RegisterWithCustomEmail(email, password string) (*RegisterResult, string, error) {
	return nil, "", fmt.Errorf("自定义邮箱模式暂不支持，请使用自动模式")
}

// CompleteRegistration 完成注册（提供验证码）
func (r *RegisterService) CompleteRegistration(signUpID, code string) (*RegisterResult, error) {
	return nil, fmt.Errorf("手动验证模式暂不支持，请使用自动模式")
}

// findBrowserPath 查找浏览器可执行文件路径
func findBrowserPath() string {
	if runtime.GOOS == "windows" {
		paths := []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	// 其他操作系统可以使用默认查找逻辑，或者在这里添加
	return ""
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// BatchRegister 批量注册（多线程）
// count: 注册数量, workers: 并发线程数, headless: 是否无头模式
func (r *RegisterService) BatchRegister(count, workers int, headless bool) *BatchRegisterResult {
	if count <= 0 {
		count = 1
	}
	if workers <= 0 {
		workers = 2
	}
	// 不再硬编码限制，由调用方决定
	if workers > count {
		workers = count
	}

	result := &BatchRegisterResult{
		Total:     count,
		Results:   make([]*RegisterResult, 0, count),
		StartTime: time.Now(),
	}

	log.Printf("[批量注册] 开始批量注册: 总数=%d, 线程数=%d, 无头模式=%v", count, workers, headless)

	// 创建任务通道和结果通道
	tasks := make(chan int, count)
	results := make(chan *RegisterResult, count)

	// 启动 worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if err := recover(); err != nil {
					log.Printf("[Worker-%d] panic recovered: %v", workerID, err)
				}
			}()
			for taskID := range tasks {
				log.Printf("[Worker-%d] 开始注册任务 #%d", workerID, taskID)

				// 只使用 mail.tm（其他提供商的邮箱 Orchids 不接受）
				tempMail := tempmail.NewTempMail()
				singleResult := r.registerSingle(tempMail, workerID, taskID, headless)
				results <- singleResult

				log.Printf("[Worker-%d] 完成注册任务 #%d, 成功=%v", workerID, taskID, singleResult.Success)
			}
		}(i + 1)
	}

	// 发送任务（增加延迟避免 mail.tm 429）
	go func() {
		for i := 0; i < count; i++ {
			tasks <- i + 1
			// 每个任务之间延迟 2 秒，让 workers 错开请求 mail.tm
			if i < count-1 {
				time.Sleep(2 * time.Second)
			}
		}
		close(tasks)
	}()

	// 等待所有 worker 完成后关闭结果通道
	go func() {
		wg.Wait()
		close(results)
	}()

	// 收集结果
	for res := range results {
		result.Results = append(result.Results, res)
		if res.Success {
			result.Success++
		} else {
			result.Failed++
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime).String()

	log.Printf("[批量注册] 完成! 总数=%d, 成功=%d, 失败=%d, 耗时=%s",
		result.Total, result.Success, result.Failed, result.Duration)

	return result
}

// registerSingle 单次注册（供 worker 调用）
func (r *RegisterService) registerSingle(tempMail tempmail.TempMailService, workerID, taskID int, headless bool) *RegisterResult {
	result := &RegisterResult{}

	// panic recovery
	defer func() {
		if err := recover(); err != nil {
			result.Error = fmt.Sprintf("panic: %v", err)
			result.Success = false
			log.Printf("[Worker-%d][任务#%d] panic: %v", workerID, taskID, err)
		}
	}()

	// 1. 生成临时邮箱
	email, err := tempMail.GenerateEmail()
	if err != nil {
		result.Error = fmt.Sprintf("生成临时邮箱失败: %v", err)
		return result
	}
	result.Email = email
	result.Password = defaultPassword
	log.Printf("[Worker-%d][任务#%d] 使用 %s 生成邮箱: %s", workerID, taskID, tempMail.Name(), email)

	// 2. 使用浏览器自动化完成注册
	clientCookie, err := r.browserRegisterWithMail(tempMail, email, defaultPassword, headless, workerID, taskID)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.ClientCookie = clientCookie
	result.Success = true
	log.Printf("[Worker-%d][任务#%d] 注册成功! Email: %s", workerID, taskID, email)

	return result
}

// browserRegisterWithMail 使用 chromedp 进行浏览器自动化注册（带 tempMail 实例）
func (r *RegisterService) browserRegisterWithMail(tempMail tempmail.TempMailService, email, password string, headless bool, workerID, taskID int) (string, error) {
	logPrefix := fmt.Sprintf("[Worker-%d][任务#%d]", workerID, taskID)

	// 创建浏览器上下文
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-gpu", false),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("window-size", "1280,800"),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("exclude-switches", "enable-automation"),
		chromedp.Flag("disable-infobars", true),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
	)

	// 自动查找浏览器路径 (优先 Edge)
	if execPath := findBrowserPath(); execPath != "" {
		opts = append(opts, chromedp.ExecPath(execPath))
		log.Printf("[自动注册] 使用浏览器: %s", execPath)
	}

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置超时
	ctx, cancel = context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var clientCookie string

	// 1. 访问首页
	log.Printf("%s 正在打开 Orchids 首页...", logPrefix)
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.Evaluate(`
				Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
				window.chrome = {runtime: {}};
				Object.defineProperty(navigator, 'languages', {get: () => ['zh-CN', 'zh', 'en']});
				Object.defineProperty(navigator, 'plugins', {get: () => [1,2,3,4,5]});
			`, nil).Do(ctx)
		}),
		chromedp.Navigate(orchidsURL),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("打开首页失败: %w", err)
	}

	// 2. 点击 Sign in 按钮
	log.Printf("%s 正在点击 Sign in...", logPrefix)
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`button[component="SignInButton"]`, chromedp.ByQuery),
		chromedp.Click(`button[component="SignInButton"]`, chromedp.ByQuery),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("点击 Sign in 失败: %w", err)
	}

	// 3. 点击 Sign up 链接
	log.Printf("%s 正在点击 Sign up...", logPrefix)
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`//a[contains(text(),'Sign up')]`, chromedp.BySearch),
		chromedp.Sleep(1*time.Second),
		chromedp.Click(`//a[contains(text(),'Sign up')]`, chromedp.BySearch),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("点击 Sign up 失败: %w", err)
	}

	// 4. 输入邮箱
	log.Printf("%s 正在输入邮箱...", logPrefix)
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`input[name="emailAddress"]`, chromedp.ByQuery),
		chromedp.Click(`input[name="emailAddress"]`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.SendKeys(`input[name="emailAddress"]`, email, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	)
	if err != nil {
		return "", fmt.Errorf("输入邮箱失败: %w", err)
	}

	// 5. 输入密码
	log.Printf("%s 正在输入密码...", logPrefix)
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`input[name="password"]`, chromedp.ByQuery),
		chromedp.Click(`input[name="password"]`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.SendKeys(`input[name="password"]`, password, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	)
	if err != nil {
		return "", fmt.Errorf("输入密码失败: %w", err)
	}

	// 6. 点击 Continue 按钮
	log.Printf("%s 正在点击 Continue...", logPrefix)
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`
			(function() {
				var buttons = document.querySelectorAll('button');
				for (var i = 0; i < buttons.length; i++) {
					var text = buttons[i].textContent.trim();
					if (text.match(/^Continue/) && !text.includes('Google')) {
						buttons[i].click();
						return true;
					}
				}
				return false;
			})()
		`, nil),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("点击 Continue 失败: %w", err)
	}

	// 7. 等待验证码页面，处理 Turnstile
	log.Printf("%s 等待验证码页面...", logPrefix)
	for i := 0; i < 30; i++ {
		var onCodePage bool
		chromedp.Run(ctx, chromedp.Evaluate(`
			document.body.innerText.includes('Verify your email') ||
			document.body.innerText.includes('verification code') ||
			!!document.querySelector('input[data-input-otp="true"]')
		`, &onCodePage))
		if onCodePage {
			log.Printf("%s 已到验证码页面", logPrefix)
			break
		}

		// 检测并点击 Turnstile
		var hasTurnstile bool
		chromedp.Run(ctx, chromedp.Evaluate(`
			(function() {
				var iframes = document.querySelectorAll('iframe');
				for (var i = 0; i < iframes.length; i++) {
					if (iframes[i].src && iframes[i].src.includes('challenges.cloudflare.com')) {
						iframes[i].click();
						return true;
					}
				}
				var container = document.querySelector('[class*="turnstile"]');
				if (container) {
					container.click();
					return true;
				}
				return false;
			})()
		`, &hasTurnstile))
		if hasTurnstile {
			log.Printf("%s 发现并点击了 Turnstile", logPrefix)
		}

		chromedp.Run(ctx, chromedp.Sleep(1*time.Second))
	}

	// 8. 获取验证码邮件
	log.Printf("%s 等待验证码邮件...", logPrefix)
	code, err := tempMail.WaitForVerificationCode(email, 120*time.Second)
	if err != nil {
		return "", fmt.Errorf("获取验证码失败: %w", err)
	}
	log.Printf("%s 获取到验证码: %s", logPrefix, code)

	// 9. 输入验证码
	log.Printf("%s 正在输入验证码...", logPrefix)
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`input[data-input-otp="true"]`, chromedp.ByQuery),
		chromedp.Click(`input[data-input-otp="true"]`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.SendKeys(`input[data-input-otp="true"]`, code, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("输入验证码失败: %w", err)
	}

	// 10. 点击 Continue 完成验证
	log.Printf("%s 点击 Continue 完成验证...", logPrefix)
	chromedp.Run(ctx,
		chromedp.Evaluate(`
			(function() {
				var buttons = document.querySelectorAll('button');
				for (var i = 0; i < buttons.length; i++) {
					var text = buttons[i].textContent.trim();
					if (text.match(/^Continue/) && !text.includes('Google')) {
						buttons[i].click();
						return true;
					}
				}
				return false;
			})()
		`, nil),
		chromedp.Sleep(5*time.Second),
	)

	// 11. 等待注册完成
	log.Printf("%s 等待注册完成...", logPrefix)
	chromedp.Run(ctx, chromedp.Sleep(5*time.Second))

	// 12. 获取 __client cookie
	log.Printf("%s 正在获取 cookie...", logPrefix)
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			cookies, err := network.GetCookies().
				WithURLs([]string{
					"https://clerk.orchids.app",
					"https://www.orchids.app",
					"https://orchids.app",
				}).Do(ctx)
			if err != nil {
				return err
			}

			for _, cookie := range cookies {
				if cookie.Name == "__client" {
					clientCookie = cookie.Value
					log.Printf("%s 获取到 __client cookie (长度: %d)", logPrefix, len(clientCookie))
					return nil
				}
			}
			return nil
		}),
	)
	if err != nil {
		return "", fmt.Errorf("获取 cookies 失败: %w", err)
	}

	if clientCookie == "" {
		return "", fmt.Errorf("未能获取 __client cookie")
	}

	return clientCookie, nil
}
