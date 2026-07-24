package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"hanime-dl/chrome"
)

// newTabContext 在已有浏览器中打开一个新「标签页」（而非新窗口）并返回附着其上的 chromedp context。
// 说明：chromedp v0.13.x 在远程 allocator 下会强制以 newWindow=true 创建新窗口，
// 这里改为先通过 DevTools 创建标签页拿到 target id，再用 WithTargetID 附着，
// 取消返回的 cancel 时 chromedp 会自动关闭该标签页。
func newTabContext(allocatorContext context.Context, wsURL string) (context.Context, context.CancelFunc, error) {
	tabID, err := chrome.CreateTab(wsURL, "about:blank", 15*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open new tab: %w", err)
	}
	ctx, cancel := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(target.ID(tabID)))
	return ctx, cancel, nil
}

const (
	chromeDownURL         = "https://hanime1.me/download?v=%s"
	downloadTitleSelector = `h3`
	downloadImageSelector = `img.download-image`

	// 导航时等待主文档响应的最长时间
	navigateStatusWait = 20 * time.Second
)

// HTTPPageError 表示页面返回了 HTTP 错误状态码（如 403/404/5xx）。
// 携带状态码供上层判断是否应该重试或刷新 URL。
type HTTPPageError struct {
	StatusCode int
	URL        string
}

func (e *HTTPPageError) Error() string {
	if e == nil {
		return "http page error"
	}
	return fmt.Sprintf("page returned HTTP %d for %s", e.StatusCode, e.URL)
}

// IsHTTPPageError 判断错误是否为页面 HTTP 状态码错误，并返回状态码。
// 上层（downloader/web_server）用此函数区分可重试与不可重试错误。
func IsHTTPPageError(err error) (int, bool) {
	var e *HTTPPageError
	if errors.As(err, &e) {
		return e.StatusCode, true
	}
	return 0, false
}

// navigateAction 返回一个 chromedp.Action：导航到 navURL 并捕获主文档的
// HTTP 状态码。若状态码 >= 400，立即返回 *HTTPPageError，避免后续
// WaitVisible 等动作在不存在的元素上长时间阻塞（这是 403/404 卡住的根因）。
//
// 实现说明：先注册 network.ResponseReceived 监听器，再执行 Navigate，
// 监听器在单独的 goroutine 中收到主文档响应后写入带缓冲的 channel，
// ActionFunc 主流程读取 channel 并判断状态码。
func navigateAction(navURL string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		statusCh := make(chan int, 4)
		chromedp.ListenTarget(ctx, func(ev interface{}) {
			respEv, ok := ev.(*network.EventResponseReceived)
			if !ok {
				return
			}
			// 只关注主文档（Document）响应，忽略图片/JS/CSS 等子资源
			if respEv.Type != network.ResourceTypeDocument {
				return
			}
			select {
			case statusCh <- int(respEv.Response.Status):
			default:
			}
		})

		// 执行导航
		if err := chromedp.Navigate(navURL).Do(ctx); err != nil {
			return fmt.Errorf("navigation failed: %w", err)
		}

		// 等待主文档响应到达
		timer := time.NewTimer(navigateStatusWait)
		defer timer.Stop()
		for {
			select {
			case code := <-statusCh:
				if code >= 400 {
					return &HTTPPageError{StatusCode: code, URL: navURL}
				}
				return nil
			case <-timer.C:
				// 导航响应超时：可能是网络问题或页面一直未触发 Document 事件。
				// 不当作成功，返回明确错误，避免后续卡住。
				return fmt.Errorf("timeout waiting for navigation response of %s", navURL)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
}

// Result 视频解析结果
type Result struct {
	Title    string `json:"title"`
	ImageURL string `json:"image_url"`
	DataURL  string `json:"data_url"`
}

// VideoMetadata 视频元数据
type VideoMetadata struct {
	VideoID       string `json:"video_id"`
	Title         string `json:"title"`
	ImageURL      string `json:"image_url"`
	DataURL       string `json:"data_url"`
	ImageFilePath string `json:"image_file_path"`
	VideoFilePath string `json:"video_file_path"`
	TargetDir     string `json:"target_dir"`
	ListID        string `json:"list_id"`
}

// Scraper 网页抓取器
type Scraper struct {
	cacheDir   string
	downDir    string
	resolution string
}

// NewScraper 创建新的抓取器
func NewScraper(cacheDir, downDir, resolution string) *Scraper {
	return &Scraper{
		cacheDir:   cacheDir,
		downDir:    downDir,
		resolution: resolution,
	}
}

// GetPlaylist 获取播放列表（带缓存）
func (s *Scraper) GetPlaylist(wsURL, videoID string) ([]string, error) {
	// 检查缓存
	cacheFile := filepath.Join(s.cacheDir, fmt.Sprintf("list_%s.json", videoID))
	if data, err := os.ReadFile(cacheFile); err == nil {
		var cachedList []string
		if err := json.Unmarshal(data, &cachedList); err == nil && len(cachedList) > 0 {
			log.Printf("Loaded playlist %s from cache (%d items)", videoID, len(cachedList))
			return cachedList, nil
		}
	}

	// 从网页获取
	ctx1, cancel1 := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel1()

	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(ctx1, wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()

	ctx, cancelCtx, err := newTabContext(allocatorContext, wsURL)
	if err != nil {
		return nil, err
	}
	defer cancelCtx()

	// 用 JS 直接提取播放列表里的所有视频 ID。
	// 真实页面结构：播放列表容器 id 为 #playlist-scroll，每个视频项外层是
	// <div class="playlist-hover-wrap clickable-row" data-href="https://hanime1.me/watch?v=xxx">，
	// data-href 即视频地址，最精确。退而求其次取容器内 a[href*="watch?v="]。
	extractJS := `(function(){
		var wrap = document.querySelector('#playlist-scroll');
		if(!wrap){ return []; }
		var nodes = wrap.querySelectorAll('.playlist-hover-wrap');
		if(nodes.length === 0){
			nodes = wrap.querySelectorAll('a[href*="watch?v="]');
		}
		var seen = {};
		var out = [];
		nodes.forEach(function(n){
			var href = n.getAttribute('data-href') || n.getAttribute('href');
			if(!href){ return; }
			try {
				var u = new URL(href, location.href);
				var v = u.searchParams.get('v');
				if(v && !seen[v]){ seen[v] = true; out.push(v); }
			} catch(e){}
		});
		return out;
	})()`

	var raw []interface{}
	err = chromedp.Run(ctx,
		// 先检测 HTTP 状态码（403/404 时快速失败，不再等待 JS 渲染）
		navigateAction(fmt.Sprintf("https://hanime1.me/watch?v=%s", videoID)),
		chromedp.Sleep(5*time.Second),
		chromedp.Evaluate(extractJS, &raw),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to fetch playlist %s: %w", videoID, err)
	}

	result := make([]string, 0, len(raw))
	seen := make(map[string]bool)
	for _, item := range raw {
		id, ok := item.(string)
		if !ok || id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("playlist %s returned no video ids (page structure may have changed)", videoID)
	}

	// 保存到缓存
	if cacheData, err := json.Marshal(result); err == nil {
		os.WriteFile(cacheFile, cacheData, 0644)
	}

	log.Printf("Fetched playlist %s: %d videos", videoID, len(result))
	return result, nil
}

// ResolveVideoInfo 解析视频信息（带缓存）
func (s *Scraper) ResolveVideoInfo(wsURL, videoID, listID string) (VideoMetadata, error) {
	// 检查缓存
	cacheFile := filepath.Join(s.cacheDir, fmt.Sprintf("info_%s.json", videoID))
	if data, err := os.ReadFile(cacheFile); err == nil {
		var cachedMeta VideoMetadata
		if err := json.Unmarshal(data, &cachedMeta); err == nil {
			log.Printf("Loaded info for %s from cache", videoID)
			cachedMeta.ListID = listID
			return cachedMeta, nil
		}
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel1()

	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(ctx1, wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()

	ctx, cancelCtx, err := newTabContext(allocatorContext, wsURL)
	if err != nil {
		return VideoMetadata{}, err
	}
	defer cancelCtx()

	var res Result
	var downloadLinks []map[string]string
	err = chromedp.Run(ctx,
		// 先检测 HTTP 状态码：403/404/5xx 时立即返回错误，
		// 避免下面的 WaitVisible 在不存在的 h3 元素上阻塞到超时（原超时 3000s，导致卡住）
		navigateAction(fmt.Sprintf(chromeDownURL, videoID)),
		chromedp.WaitVisible(downloadTitleSelector, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.Text(downloadTitleSelector, &res.Title, chromedp.NodeVisible, chromedp.ByQuery),
		chromedp.AttributeValue(downloadImageSelector, "src", &res.ImageURL, nil, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('table.download-table tr')).slice(1).map(tr => {
				let resTd = tr.querySelector('td:nth-child(2)');
				let linkA = tr.querySelector('td:nth-child(5) a');
				if (resTd && linkA) {
					return {
						resolution: resTd.innerText.trim(),
						url: linkA.getAttribute('data-url')
					};
				}
				return null;
			}).filter(x => x !== null)
		`, &downloadLinks),
	)

	if err != nil {
		return VideoMetadata{}, err
	}

	// 选择分辨率
	selectedURL := ""
	if len(downloadLinks) > 0 {
		if s.resolution != "" {
			for _, linkInfo := range downloadLinks {
				if strings.Contains(linkInfo["resolution"], s.resolution) {
					selectedURL = linkInfo["url"]
					log.Printf("Found requested resolution %s for %s", s.resolution, videoID)
					break
				}
			}
		}
		if selectedURL == "" {
			selectedURL = downloadLinks[0]["url"]
			if s.resolution != "" {
				log.Printf("Requested resolution %s not found for %s, falling back to: %s", s.resolution, videoID, downloadLinks[0]["resolution"])
			}
		}
		res.DataURL = selectedURL
	}

	if res.Title == "" || res.DataURL == "" {
		return VideoMetadata{}, fmt.Errorf("missing title or data url")
	}

	// 尝试获取高清封面图
	searchURL := fmt.Sprintf("https://hanime1.me/search?query=%s",
		url.QueryEscape(strings.TrimSpace(res.Title))) + "&type=&genre=%E8%A3%85%E7%95%8C&sort=&date=&duration="

	var searchImgUrl string
	searchCtx, searchCancel := context.WithTimeout(ctx, 15*time.Second)
	defer searchCancel()

	searchErr := chromedp.Run(searchCtx,
		chromedp.Navigate(searchURL),
		chromedp.Sleep(2*time.Second),
		chromedp.Evaluate(fmt.Sprintf(`
			new Promise((resolve) => {
				let attempts = 0;
				let check = () => {
					attempts++;
					let link = document.querySelector("a[href*='watch?v=%s']");
					if (link) {
						let img = link.querySelector("img");
						if (img && img.src) {
							resolve(img.src);
							return;
						}
					}
					if (attempts >= 25) {
						resolve("");
						return;
					}
					setTimeout(check, 200);
				};
				check();
			});
		`, videoID), &searchImgUrl, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	if searchErr == nil && searchImgUrl != "" {
		res.ImageURL = searchImgUrl
		log.Printf("Found high-res cover image from search for %s", videoID)
	}

	// 构建元数据
	title := strings.TrimSpace(res.Title)
	var dirName string

	if strings.HasPrefix(title, "(") {
		if idx := strings.Index(title, ")"); idx != -1 {
			dirName = title[:idx+1]
		}
	} else if strings.HasPrefix(title, "[") {
		if idx := strings.Index(title, "]"); idx != -1 {
			dirName = title[:idx+1]
		}
	}

	if dirName == "" {
		dirName = strings.Split(title, " ")[0]
	}

	dirName = sanitizeFilename(dirName, 200, "unnamed")

	fullDir := filepath.Join(s.downDir, dirName)
	os.MkdirAll(fullDir, 0755)

	// 文件名格式：[视频ID]标题，如 [407238]ルーシーとモルス
	// 先拼接前缀和标题，再统一做 Windows 安全化处理（截断、非法字符替换等）。
	// 前缀 [视频ID] 是 ASCII，在 sanitizeFilename 的 200 字节截断中不会被破坏
	// （最坏情况下截断发生在标题部分，前缀完整保留）。
	fileNameBase := sanitizeFilename(fmt.Sprintf("[%s]%s", videoID, title), 200, "unnamed")

	meta := VideoMetadata{
		VideoID:       videoID,
		Title:         res.Title,
		ImageURL:      res.ImageURL,
		DataURL:       res.DataURL,
		TargetDir:     fullDir,
		ImageFilePath: filepath.Join(fullDir, fileNameBase+".jpg"),
		VideoFilePath: filepath.Join(fullDir, fileNameBase+".mp4"),
		ListID:        listID,
	}

	// 保存到缓存
	if cacheData, err := json.MarshalIndent(meta, "", "  "); err == nil {
		os.WriteFile(cacheFile, cacheData, 0644)
	}

	return meta, nil
}

// RefreshVideoDataURL 刷新视频下载 URL
func (s *Scraper) RefreshVideoDataURL(wsURL, videoID string) (string, error) {
	ctx1, cancel1 := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel1()

	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(ctx1, wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()

	ctx, cancelCtx, err := newTabContext(allocatorContext, wsURL)
	if err != nil {
		return "", err
	}
	defer cancelCtx()

	var res Result
	var downloadLinks []map[string]string
	err = chromedp.Run(ctx,
		// 先检测 HTTP 状态码：403/404/5xx 时立即返回错误
		navigateAction(fmt.Sprintf(chromeDownURL, videoID)),
		chromedp.WaitVisible(downloadTitleSelector, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.Text(downloadTitleSelector, &res.Title, chromedp.NodeVisible, chromedp.ByQuery),
		chromedp.AttributeValue(downloadImageSelector, "src", &res.ImageURL, nil, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('table.download-table tr')).slice(1).map(tr => {
				let resTd = tr.querySelector('td:nth-child(2)');
				let linkA = tr.querySelector('td:nth-child(5) a');
				if (resTd && linkA) {
					return {
						resolution: resTd.innerText.trim(),
						url: linkA.getAttribute('data-url')
					};
				}
				return null;
			}).filter(x => x !== null)
		`, &downloadLinks),
	)

	if err != nil {
		return "", err
	}

	if len(downloadLinks) == 0 {
		return "", fmt.Errorf("no download links found")
	}

	selectedURL := ""
	if s.resolution != "" {
		for _, linkInfo := range downloadLinks {
			if strings.Contains(linkInfo["resolution"], s.resolution) {
				selectedURL = linkInfo["url"]
				break
			}
		}
	}
	if selectedURL == "" {
		selectedURL = downloadLinks[0]["url"]
	}

	if selectedURL == "" {
		return "", fmt.Errorf("resolved data url is empty")
	}

	// 更新缓存
	cacheFile := filepath.Join(s.cacheDir, fmt.Sprintf("info_%s.json", videoID))
	if data, err := os.ReadFile(cacheFile); err == nil {
		var cachedMeta VideoMetadata
		if err := json.Unmarshal(data, &cachedMeta); err == nil {
			cachedMeta.DataURL = selectedURL
			if res.Title != "" {
				cachedMeta.Title = res.Title
			}
			if res.ImageURL != "" {
				cachedMeta.ImageURL = res.ImageURL
			}
			if cacheData, err := json.MarshalIndent(cachedMeta, "", "  "); err == nil {
				os.WriteFile(cacheFile, cacheData, 0644)
			}
		}
	}

	log.Printf("Refreshed DataURL for %s (%s)", videoID, res.Title)
	return selectedURL, nil
}

// ResolutionLink 分辨率下载链接
type ResolutionLink struct {
	Resolution string
	URL        string
}

// resolutionPriority 定义分辨率优先级顺序（从高到低）
var resolutionPriority = []string{"1080p", "720p", "480p", "360p", "240p"}

// normalizeResolution 将分辨率文本标准化为小写无空格形式，并匹配到标准分辨率。
// 例如 "1080P"、"1080 p"、"1920x1080" 都会归一化为 "1080p"。
func normalizeResolution(res string) string {
	res = strings.ToLower(strings.TrimSpace(res))
	res = strings.ReplaceAll(res, " ", "")
	for _, std := range resolutionPriority {
		if strings.Contains(res, std) {
			return std
		}
	}
	return res
}

// sortLinksByPriority 将下载链接按分辨率优先级排序。
// 从配置的分辨率开始，逐级降级到 240p。
// 例如配置为 1080p 时排序为: 1080p → 720p → 480p → 360p → 240p
// 配置为 720p 时排序为: 720p → 480p → 360p → 240p（不尝试 1080p）
func sortLinksByPriority(links []ResolutionLink, preferredRes string) []ResolutionLink {
	preferredNorm := normalizeResolution(preferredRes)

	// 找到配置分辨率的起始索引
	startIdx := 0
	for i, r := range resolutionPriority {
		if r == preferredNorm {
			startIdx = i
			break
		}
	}

	// 建立标准化分辨率 → 链接的映射
	linkMap := make(map[string]ResolutionLink)
	for _, l := range links {
		norm := normalizeResolution(l.Resolution)
		if _, exists := linkMap[norm]; !exists {
			linkMap[norm] = l
		}
	}

	// 从配置分辨率开始，按降序排列
	var sorted []ResolutionLink
	for i := startIdx; i < len(resolutionPriority); i++ {
		if l, ok := linkMap[resolutionPriority[i]]; ok {
			sorted = append(sorted, l)
		}
	}

	// 添加未在标准列表中的分辨率（排到最后）
	for _, l := range links {
		norm := normalizeResolution(l.Resolution)
		found := false
		for _, r := range resolutionPriority {
			if r == norm {
				found = true
				break
			}
		}
		if !found {
			sorted = append(sorted, l)
		}
	}

	return sorted
}

// FallbackResolveDownloadURLs 通过 watch 页面点击下载按钮获取下载链接（降级方案）。
//
// 流程：
//  1. 导航到 watch 页面 (https://hanime1.me/watch?v=XXX)，建立会话
//  2. 点击下载按钮（若按钮隐藏在 more_horiz 下拉菜单中则先展开菜单再点击）
//  3. 等待跳转到下载页面，若未跳转则直接导航到下载页面
//  4. 提取所有分辨率的下载链接
//  5. 按优先级排序返回（配置分辨率 → 720p → 480p → 360p → 240p）
//
// 调用方（main.go / web_server.go）拿到链接后逐个尝试下载，直到成功或全部失败。
// 同时返回从下载页面提取的封面图 URL（可能为空），调用方可用于刷新过期的 ImageURL。
func (s *Scraper) FallbackResolveDownloadURLs(wsURL, videoID string) ([]ResolutionLink, string, error) {
	ctx1, cancel1 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel1()

	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(ctx1, wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()

	ctx, cancelCtx, err := newTabContext(allocatorContext, wsURL)
	if err != nil {
		return nil, "", err
	}
	defer cancelCtx()

	// 1. 导航到 watch 页面（建立会话/cookie）
	watchURL := fmt.Sprintf("https://hanime1.me/watch?v=%s", videoID)
	err = chromedp.Run(ctx,
		navigateAction(watchURL),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return nil, "", fmt.Errorf("fallback: navigate to watch page failed: %w", err)
	}

	// 2. 尝试点击下载按钮
	//    优先点击可见的 #video-download-btn
	//    若不可见（hidden-xs），则点击 more_horiz 展开下拉菜单再点击下载项
	clickResult := ""
	_ = chromedp.Run(ctx,
		chromedp.Evaluate(`(function() {
			// 尝试直接点击可见的下载按钮
			var btn = document.querySelector('#video-download-btn');
			if (btn) {
				var parent = btn.closest('.video-show-action-btn');
				if (parent) {
					var style = window.getComputedStyle(parent);
					if (style.display !== 'none' && style.visibility !== 'hidden' && parent.offsetWidth > 0) {
						btn.click();
						return 'direct';
					}
				}
			}
			// 下载按钮可能隐藏在 more_horiz 下拉菜单中
			var dropdownToggle = document.querySelector('.video-show-action-btn.dropdown-toggle');
			if (dropdownToggle) {
				dropdownToggle.click();
				return 'dropdown';
			}
			return 'none';
		})()`, &clickResult),
	)

	log.Printf("Fallback: download button click result for %s: %s", videoID, clickResult)

	if clickResult == "dropdown" {
		// 等待下拉菜单展开，然后点击下载项
		time.Sleep(1 * time.Second)
		_ = chromedp.Run(ctx,
			chromedp.Evaluate(`(function() {
				var items = document.querySelectorAll('.more-horiz-item');
				for (var i = 0; i < items.length; i++) {
					var span = items[i].querySelector('span');
					var icon = items[i].querySelector('.material-icons');
					if (span && icon && icon.textContent.trim() === 'download') {
						items[i].click();
						return true;
					}
				}
				return false;
			})()`, nil),
		)
	}

	// 3. 等待页面跳转，检查是否已到达下载页面
	time.Sleep(3 * time.Second)

	var hasTable bool
	_ = chromedp.Run(ctx,
		chromedp.Evaluate(`!!document.querySelector('table.download-table')`, &hasTable),
	)

	if !hasTable {
		// 点击没有成功跳转，直接导航到下载页面
		log.Printf("Fallback: click did not navigate, falling back to direct download page for %s", videoID)
		downloadURL := fmt.Sprintf(chromeDownURL, videoID)
		err = chromedp.Run(ctx,
			navigateAction(downloadURL),
			chromedp.Sleep(2*time.Second),
		)
		if err != nil {
			return nil, "", fmt.Errorf("fallback: navigate to download page failed: %w", err)
		}
	}

	// 4. 一次性提取下载链接 + 封面图 URL（原子操作，避免页面状态变化）
	type fallbackData struct {
		Links    []map[string]string `json:"links"`
		ImageURL string              `json:"imageURL"`
	}
	var data fallbackData
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`table.download-table`, chromedp.ByQuery),
		chromedp.Sleep(1*time.Second),
		chromedp.Evaluate(`
			(function() {
				var links = Array.from(document.querySelectorAll('table.download-table tr')).slice(1).map(tr => {
					let resTd = tr.querySelector('td:nth-child(2)');
					let linkA = tr.querySelector('td:nth-child(5) a');
					if (resTd && linkA) {
						return {
							resolution: resTd.innerText.trim(),
							url: linkA.getAttribute('data-url')
						};
					}
					return null;
				}).filter(x => x !== null);

				var img = document.querySelector('img.download-image');
				var imageURL = "";
				if (img) {
					imageURL = img.getAttribute('src') || img.src || "";
				}

				return { links: links, imageURL: imageURL };
			})()
		`, &data),
	)
	if err != nil {
		return nil, "", fmt.Errorf("fallback: extract download data failed: %w", err)
	}

	if len(data.Links) == 0 {
		return nil, "", fmt.Errorf("fallback: no download links found")
	}

	imageURL := data.ImageURL

	// 如果下载页面没有封面图，尝试从搜索页面获取
	if imageURL == "" {
		log.Printf("Fallback: no cover image on download page, trying search for %s", videoID)
		searchURL := fmt.Sprintf("https://hanime1.me/search?query=%s&type=&genre=&sort=&date=&duration=",
			url.QueryEscape(videoID))
		_ = chromedp.Run(ctx,
			chromedp.Navigate(searchURL),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(fmt.Sprintf(`
				(function() {
					var link = document.querySelector("a[href*='watch?v=%s']");
					if (link) {
						var img = link.querySelector("img");
						if (img) { return img.getAttribute('src') || img.src || ""; }
					}
					return "";
				})()
			`, videoID), &imageURL),
		)
	}

	if imageURL != "" {
		log.Printf("Fallback: extracted cover image URL for %s: %s", videoID, imageURL)
	} else {
		log.Printf("Fallback: no cover image URL found for %s", videoID)
	}

	// 5. 按分辨率优先级排序（从配置分辨率开始，逐级降级到 240p）
	links := make([]ResolutionLink, 0, len(data.Links))
	for _, l := range data.Links {
		if l["url"] != "" {
			links = append(links, ResolutionLink{
				Resolution: strings.TrimSpace(l["resolution"]),
				URL:        l["url"],
			})
		}
	}

	sortedLinks := sortLinksByPriority(links, s.resolution)

	log.Printf("Fallback: resolved %d download links for %s (priority: %s → 240p)",
		len(sortedLinks), videoID, s.resolution)
	for i, l := range sortedLinks {
		log.Printf("Fallback: [%d] %s for %s", i+1, l.Resolution, videoID)
	}

	return sortedLinks, imageURL, nil
}

// truncateFilename 截断文件名
func truncateFilename(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			s = s[:len(s)-1]
		} else {
			break
		}
	}
	return s
}

// windowsIllegalChars 是 Windows 文件名中禁止使用的字符集合。
// 包含 < > : " / \ | ? * 以及控制字符 (0x00-0x1F)。
// 参考: https://learn.microsoft.com/windows/win32/fileio/naming-a-file
var windowsIllegalChars = func() map[rune]bool {
	m := make(map[rune]bool)
	// 注意: 不能用含空格的字符串 range（会把分隔空格也加入），
	// 必须用 rune 切片显式列出每个非法字符。
	for _, c := range []rune{'<', '>', ':', '"', '/', '\\', '|', '?', '*'} {
		m[c] = true
	}
	for r := rune(0x00); r <= 0x1F; r++ {
		m[r] = true
	}
	return m
}()

// sanitizeFilename 对文件名进行 Windows 安全化处理，依次执行：
//  1. 将所有 Windows 非法字符（< > : " / \ | ? * 及控制字符）替换为下划线
//  2. UTF-8 安全截断到 maxBytes 字节（避免在多字节字符中间截断产生乱码）
//  3. 去除末尾空格和点号（Windows 不允许文件名以空格或点号结尾，会导致文件无法创建或扩展名被误判）
//  4. 处理后为空时返回 fallback（如 "unnamed"），避免生成空文件名
//
// 注意：末尾清理必须在截断之后，因为截断可能产生新的末尾空格/点号。
func sanitizeFilename(s string, maxBytes int, fallback string) string {
	// 1. 替换 Windows 非法字符为下划线
	s = strings.Map(func(r rune) rune {
		if windowsIllegalChars[r] {
			return '_'
		}
		return r
	}, s)

	// 2. UTF-8 安全截断到 maxBytes 字节
	s = truncateFilename(s, maxBytes)

	// 3. 去除末尾空格和点号（Windows 文件系统限制）
	s = strings.TrimRight(s, " .")

	// 4. 空值兜底（原字符串全为非法字符或清理后为空时）
	if s == "" {
		return fallback
	}
	return s
}
