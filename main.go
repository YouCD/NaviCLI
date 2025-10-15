package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/spf13/viper"
	"github.com/wildeyedskies/go-mpv/mpv"
	"github.com/yhkl-dev/NaviCLI/mpvplayer"
	"github.com/yhkl-dev/NaviCLI/subsonic"
)

func formatDuration(seconds int) string {
	minutes := seconds / 60
	seconds = seconds % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

type Application struct {
	application    *tview.Application
	subsonicClient *subsonic.Client
	mpvInstance    *mpvplayer.Mpvplayer
	totalSongs     []subsonic.Song
	currentPage    int
	pageSize       int
	totalPages     int

	rootFlex    *tview.Flex
	songTable   *tview.Table
	statusBar   *tview.TextView
	progressBar *tview.TextView
	statsBar    *tview.TextView
	currentSong *subsonic.Song
	isPlaying   bool
	isLoading   bool
	loadingMux  sync.Mutex

	currentSongIndex int
	isSearching      bool
	searchMux        sync.Mutex
}

func (a *Application) setupPagination() {
	a.pageSize = 500
	a.currentPage = 1
	a.currentSongIndex = -1
	a.isLoading = false
}

func (a *Application) playSongAtIndex(index int) {
	if index < 0 || index >= len(a.totalSongs) {
		return
	}

	a.loadingMux.Lock()
	if a.isLoading {
		a.loadingMux.Unlock()
		return
	}
	a.isLoading = true
	a.currentSongIndex = index
	currentTrack := a.totalSongs[index]
	a.currentSong = &currentTrack
	a.isPlaying = false
	a.loadingMux.Unlock()

	loadingBar := "[yellow]▓▓▓[darkgray]░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ Loading..."
	info := fmt.Sprintf(`
[white]Current %d:
[yellow]%s [darkgray](Loading...)

[darkgray][play] %s
[darkgray][source] %.1f MB
[darkgray][favourite]

[gray]%s - %s
[gray]%s
%s`,
		index+1,
		currentTrack.Title,
		formatDuration(currentTrack.Duration),
		float64(currentTrack.Size)/1024/1024,
		currentTrack.Artist,
		currentTrack.Album,
		currentTrack.Album,
		loadingBar)

	a.application.QueueUpdateDraw(func() {
		if a.statusBar != nil {
			a.statusBar.SetText(info)
		}
	})

	go func() {
		defer func() {
			a.loadingMux.Lock()
			a.isLoading = false
			a.loadingMux.Unlock()

			if r := recover(); r != nil {

				a.isPlaying = false
				a.application.QueueUpdateDraw(func() {
					if a.statusBar != nil {
						failedInfo := fmt.Sprintf(`
[white]Current %d:
[red]%s [darkgray](Failed)

[darkgray][play] %s
[darkgray][source] %.1f MB
[darkgray][favourite]

[gray]%s - %s
[gray]%s
[red]Play Failed`,
							index+1,
							currentTrack.Title,
							formatDuration(currentTrack.Duration),
							float64(currentTrack.Size)/1024/1024,
							currentTrack.Artist,
							currentTrack.Album,
							currentTrack.Album)
						a.statusBar.SetText(failedInfo)
					}
				})
			}
		}()

		done := make(chan string, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {

					done <- ""
				}
			}()
			url := a.subsonicClient.GetPlayURL(currentTrack.ID)
			done <- url
		}()

		var playURL string
		select {
		case playURL = <-done:
			if playURL == "" {

				return
			}
		case <-time.After(10 * time.Second):

			return
		}

		if a.mpvInstance != nil {
			a.mpvInstance.Queue = []mpvplayer.QueueItem{{
				Id:       currentTrack.ID,
				Uri:      playURL,
				Title:    currentTrack.Title,
				Artist:   currentTrack.Artist,
				Duration: currentTrack.Duration,
			}}

			if a.mpvInstance.Mpv != nil {
				a.mpvInstance.Stop()
				time.Sleep(50 * time.Millisecond)
			}

			if a.mpvInstance.Mpv != nil {
				a.mpvInstance.Play(playURL)

				a.isPlaying = true

				playingBar := "[lightgreen]▓[darkgray]░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ 0.0%"
				playingInfo := fmt.Sprintf(`
[white]Current %d:
[lightgreen]%s

[darkgray][play] %s
[darkgray][source] %.1f MB
[darkgray][favourite]

[gray]%s - %s
[gray]%s
%s`,
					index+1,
					currentTrack.Title,
					formatDuration(currentTrack.Duration),
					float64(currentTrack.Size)/1024/1024,
					currentTrack.Artist,
					currentTrack.Album,
					currentTrack.Album,
					playingBar)

				a.application.QueueUpdateDraw(func() {
					if a.statusBar != nil {
						a.statusBar.SetText(playingInfo)
					}
				})

				time.Sleep(500 * time.Millisecond)
			}
		}
	}()
}

func (a *Application) playNextSong() {
	if len(a.totalSongs) == 0 {
		return
	}

	a.loadingMux.Lock()
	isCurrentlyLoading := a.isLoading
	a.loadingMux.Unlock()

	if isCurrentlyLoading {

		return
	}

	nextIndex := a.currentSongIndex + 1
	if nextIndex >= len(a.totalSongs) {
		nextIndex = 0
	}

	go a.playSongAtIndex(nextIndex)
}

func (a *Application) playPreviousSong() {
	if len(a.totalSongs) == 0 {
		return
	}

	a.loadingMux.Lock()
	isCurrentlyLoading := a.isLoading
	a.loadingMux.Unlock()

	if isCurrentlyLoading {

		return
	}

	prevIndex := a.currentSongIndex - 1
	if prevIndex < 0 {
		prevIndex = len(a.totalSongs) - 1
	}

	go a.playSongAtIndex(prevIndex)
}

func (a *Application) getCurrentPageData() []subsonic.Song {
	start := (a.currentPage - 1) * a.pageSize
	end := min(start+a.pageSize, len(a.totalSongs))
	return a.totalSongs[start:end]
}

func (a *Application) updateProgressBar() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if a.application == nil || a.mpvInstance == nil {
				return
			}

			a.loadingMux.Lock()
			isCurrentlyLoading := a.isLoading
			currentSongPtr := a.currentSong
			currentIndex := a.currentSongIndex
			isCurrentlyPlaying := a.isPlaying
			a.loadingMux.Unlock()

			if isCurrentlyLoading {

				continue
			}

			if a.mpvInstance.Mpv == nil {
				a.application.QueueUpdateDraw(func() {
					if a.progressBar != nil {
						idleDisplay := `
[darkgray][about] [darkgray][credits] [darkgray][rss.xml]
[darkgray][patreon] [darkgray][podcasts.apple]
[darkgray][folder.jpg] [darkgray][enterprise mode]
[darkgray][invert] [darkgray][fullscreen]`
						a.progressBar.SetText(idleDisplay)
					}
				})
				continue
			}

			if !isCurrentlyPlaying {
				if currentSongPtr != nil {
					// 获取音量信息用于暂停状态显示
					volumeDisplay := "100%"
					if a.mpvInstance.Mpv != nil {
						if vol, err := a.mpvInstance.GetProperty("volume", mpv.FORMAT_DOUBLE); err == nil {
							volumeDisplay = fmt.Sprintf("%.0f%%", vol.(float64))
						}
						if mute, err := a.mpvInstance.GetProperty("mute", mpv.FORMAT_FLAG); err == nil && mute.(bool) {
							volumeDisplay = "MUTE"
						}
					}

					a.application.QueueUpdateDraw(func() {
						if a.progressBar != nil && a.statusBar != nil {
							pausedDisplay := fmt.Sprintf(`
[darkgray]00:00:00 [darkgray][v-] [darkgray]%s [darkgray][v+] [darkgray][random]`, volumeDisplay)
							a.progressBar.SetText(pausedDisplay)

							progressBar := "[darkgray]▓▓▓▓▓▓▓▓░░░░░░░░░░░░░░░░░░░░░░ 0%"
							statusInfo := fmt.Sprintf(`
[white]Episode %d:
[yellow]%s [darkgray](PAUSED)

[darkgray][play] %s
[darkgray][source] %.1f MB
[darkgray][favourite]

[gray]%s - %s
[gray]%s
%s`,
								currentIndex+1,
								currentSongPtr.Title,
								formatDuration(currentSongPtr.Duration),
								float64(currentSongPtr.Size)/1024/1024,
								currentSongPtr.Artist,
								currentSongPtr.Album,
								currentSongPtr.Album,
								progressBar)
							a.statusBar.SetText(statusInfo)
						}
					})
				}
				continue
			}

			go func() {
				defer func() {
					if r := recover(); r != nil {
					}
				}()

				if a.mpvInstance == nil || a.mpvInstance.Mpv == nil {
					return
				}

				done := make(chan struct{})
				var currentPos, totalDuration float64
				var volume float64 = 100
				var isMuted = false
				var hasError bool

				go func() {
					defer func() {
						if r := recover(); r != nil {
							hasError = true
						}
						close(done)
					}()

					pos, err := a.mpvInstance.GetProperty("time-pos", mpv.FORMAT_DOUBLE)
					if err != nil {
						hasError = true
						return
					}
					duration, err := a.mpvInstance.GetProperty("duration", mpv.FORMAT_DOUBLE)
					if err != nil {
						hasError = true
						return
					}

					// 获取音量和静音状态
					if vol, err := a.mpvInstance.GetProperty("volume", mpv.FORMAT_DOUBLE); err == nil {
						volume = vol.(float64)
					}
					if mute, err := a.mpvInstance.GetProperty("mute", mpv.FORMAT_FLAG); err == nil {
						isMuted = mute.(bool)
					}

					currentPos = pos.(float64)
					totalDuration = duration.(float64)
				}()

				select {
				case <-done:
					if hasError {
						return
					}
				case <-time.After(200 * time.Millisecond):
					return
				}

				if totalDuration <= 0 || currentPos < 0 {
					return
				}

				currentTime := formatDuration(int(currentPos))
				totalTime := formatDuration(int(totalDuration))

				progress := currentPos / totalDuration
				if progress > 1 {
					progress = 1
				} else if progress < 0 {
					progress = 0
				}

				progressBarWidth := 30
				filledWidth := int(progress * float64(progressBarWidth))
				progressBar := ""

				for i := range progressBarWidth {
					if i < filledWidth {
						progressBar += "[lightgreen]▓"
					} else {
						progressBar += "[darkgray]░"
					}
				}
				progressBar += fmt.Sprintf("[white] %.1f%%", progress*100)

				// 格式化音量显示
				volumeDisplay := fmt.Sprintf("%.0f%%", volume)
				if isMuted {
					volumeDisplay = "MUTE"
				}

				progressText := fmt.Sprintf(`
[darkgray]%s/%s [darkgray][v-] [white]%s[darkgray] [v+] [random]`,
					currentTime, totalTime, volumeDisplay)

				select {
				case <-time.After(10 * time.Millisecond):
					return
				default:
					a.application.QueueUpdateDraw(func() {
						if a.progressBar != nil {
							a.progressBar.SetText(progressText)
						}

						if currentSongPtr != nil && a.statusBar != nil {
							statusInfo := fmt.Sprintf(`
[white]Current %d:
[lightgreen]%s

[darkgray][play] %s
[darkgray][source] %.1f MB
[darkgray][favourite]

[gray]%s - %s
[gray]%s
%s`,
								currentIndex+1,
								currentSongPtr.Title,
								formatDuration(currentSongPtr.Duration),
								float64(currentSongPtr.Size)/1024/1024,
								currentSongPtr.Artist,
								currentSongPtr.Album,
								currentSongPtr.Album,
								progressBar)
							a.statusBar.SetText(statusInfo)
						}
					})
				}
			}()

		case <-time.After(15 * time.Second):
			if a.application == nil {
				return
			}
		}
	}
}

func (a *Application) createHomepage() {
	a.progressBar = tview.NewTextView().
		SetDynamicColors(true)
	a.progressBar.SetBorder(false)

	a.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(false).
		SetWrap(true)
	a.statusBar.SetBorder(false)

	a.statsBar = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true)
	a.statsBar.SetBorder(false)

	a.songTable = tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)

	a.songTable.SetBorder(false)

	headerStyle := tcell.StyleDefault.Foreground(tcell.ColorGray).Attributes(tcell.AttrBold)

	a.songTable.SetCell(0, 0, tview.NewTableCell("").
		SetStyle(headerStyle))
	a.songTable.SetCell(0, 1, tview.NewTableCell("").
		SetStyle(headerStyle))
	a.songTable.SetCell(0, 2, tview.NewTableCell("").
		SetStyle(headerStyle))
	a.songTable.SetCell(0, 3, tview.NewTableCell("").
		SetStyle(headerStyle))
	a.songTable.SetCell(0, 4, tview.NewTableCell("").
		SetStyle(headerStyle))

	a.songTable.SetSelectedFunc(func(row, column int) {
		if row > 0 && row-1 < len(a.totalSongs) {
			a.loadingMux.Lock()
			isCurrentlyLoading := a.isLoading
			a.loadingMux.Unlock()

			if isCurrentlyLoading {

				return
			}
			go a.playSongAtIndex(row - 1)
		}
	})

	leftPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.statusBar, 0, 1, false)

	rightPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.songTable, 0, 1, true)

	mainLayout := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(leftPanel, 0, 1, false).
		AddItem(rightPanel, 0, 2, true)

	a.rootFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(mainLayout, 0, 1, true).
		AddItem(a.progressBar, 3, 0, false)

	a.application.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune {
			switch event.Rune() {
			case ' ':
				if a.mpvInstance == nil || a.mpvInstance.Mpv == nil {
					return nil
				}

				go func() {
					defer func() {
						if r := recover(); r != nil {

						}
					}()

					if a.isPlaying {
						a.mpvInstance.Pause()
						a.isPlaying = false
						if a.currentSong != nil {
							info := fmt.Sprintf(`
[white]Current %d:
[yellow]%s [darkgray](PAUSED)

[darkgray][play] %s
[darkgray][source] %.1f MB
[darkgray][favourite]

[gray]%s - %s
[gray]%s
[darkgray]▓▓▓▓▓▓▓▓░░░░░░░░░░░░░░░░░░░░░░ --%%`,
								a.currentSongIndex+1,
								a.currentSong.Title,
								formatDuration(a.currentSong.Duration),
								float64(a.currentSong.Size)/1024/1024,
								a.currentSong.Artist,
								a.currentSong.Album,
								a.currentSong.Album)

							a.application.QueueUpdateDraw(func() {
								if a.statusBar != nil {
									a.statusBar.SetText(info)
								}
							})
						}
					} else {
						a.mpvInstance.Pause()
						a.isPlaying = true
						if a.currentSong != nil {
							info := fmt.Sprintf(`
[white]Current %d:
[lightgreen]%s

[darkgray][play] %s
[darkgray][source] %.1f MB
[darkgray][favourite]

[gray]%s - %s
[gray]%s
[lightgreen]▓▓▓▓▓▓▓▓░░░░░░░░░░░░░░░░░░░░░░ --%%`,
								a.currentSongIndex+1,
								a.currentSong.Title,
								formatDuration(a.currentSong.Duration),
								float64(a.currentSong.Size)/1024/1024,
								a.currentSong.Artist,
								a.currentSong.Album,
								a.currentSong.Album)

							a.application.QueueUpdateDraw(func() {
								if a.statusBar != nil {
									a.statusBar.SetText(info)
								}
							})
						}
					}
				}()
				return nil
			case 'n', 'N':
				a.playNextSong()
				return nil
			case 'p', 'P':
				a.playPreviousSong()
				return nil
			case '+', '=': // 增加音量
				a.SetVolume(true)
				return nil
			case '-', '_': // 减少音量
				a.SetVolume(false)
				return nil
			case 'm', 'M': // 静音切换
				a.muteButton()
				return nil
			case '/': // 添加搜索功能
				a.search()
				return nil
			case 'q': // 添加搜索功能
				go func() {
					if err := a.loadMusic(); err != nil {
						a.application.QueueUpdateDraw(func() {
							a.statusBar.SetText("[red]load music failed: " + err.Error())
						})
					}
				}()
			}
		}

		switch event.Key() {
		case tcell.KeyEsc, tcell.KeyCtrlC:
			// 检查是否处于搜索模式
			a.searchMux.Lock()
			isSearching := a.isSearching
			a.searchMux.Unlock()

			if isSearching {
				// 如果在搜索模式下，取消搜索而不是退出程序
				a.application.SetRoot(a.rootFlex, true)
				a.searchMux.Lock()
				a.isSearching = false
				a.searchMux.Unlock()
				return nil
			}
			log.Println("user request exit program")

			if a.mpvInstance != nil && a.mpvInstance.Mpv != nil {
				a.mpvInstance.Command([]string{"quit"})
			}

			a.application.Stop()

			go func() {
				time.Sleep(1 * time.Second)
				os.Exit(0)
			}()
			return nil
		case tcell.KeyRight:
			a.playNextSong()
			return nil
		case tcell.KeyLeft:
			a.playPreviousSong()
			return nil
		}
		return event
	})
	a.application.SetRoot(a.rootFlex, true)

	welcomeMsg := fmt.Sprintf(`
[white]Current:
[lightgreen]Welcome to NaviCLI

[darkgray][play] Ready
[darkgray][source] Navidrome
[darkgray][favourite]

[gray]Press SPACE to play/pause
[gray]Press N/P or ←/→ for prev/next
[gray]Press ESC to exit
[gray]Select a track to start

[darkgray][red]func[darkgray] [green]navicli[darkgray]([yellow]task[darkgray] [lightblue]string[darkgray]) [lightblue]string[darkgray] {
[darkgray]    [red]return[darkgray] "^A series of mixes for listening while" [red]+[darkgray] task [red]+[darkgray] \
[darkgray]         "to focus the brain and i nspire the mind.[darkgray]"
[darkgray]}
[darkgray]
[darkgray]task := "[yellow]programming[darkgray]"

[darkgray]// %d songs
[darkgray]// Written by github.com/yhkl-dev
[darkgray]// Ready to play
[darkgray]// Auto-play next enabled`, len(a.totalSongs))
	a.statusBar.SetText(welcomeMsg)
}

func (a *Application) search() {
	searchInput, pages := a.searchUI()
	a.application.SetRoot(pages, true)
	a.application.SetFocus(searchInput)
}

func (a *Application) searchUI() (*tview.InputField, *tview.Pages) {
	// 创建搜索输入框
	searchInput := tview.NewInputField().
		SetLabel("Search: ").
		SetFieldWidth(30)

	// 创建一个自定义的模态框来容纳搜索输入框
	modalFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().
			SetDirection(tview.FlexColumn).
			AddItem(nil, 0, 1, false).
			AddItem(searchInput, 40, 0, true).
			AddItem(nil, 0, 1, false), 3, 0, true).
		AddItem(nil, 0, 1, false)

	// 设置背景色实现半透明效果
	modalFlex.SetBackgroundColor(tcell.ColorBlack)
	modalFlex.SetBackgroundColor(tcell.GetColor("rgba(0,0,0,0.5)"))

	// 设置搜索输入框的完成函数
	searchInput.SetDoneFunc(func(key tcell.Key) {
		a.searchMux.Lock()
		a.isSearching = false
		a.searchMux.Unlock()

		if key == tcell.KeyEnter {
			searchText := searchInput.GetText()
			if searchText != "" {
				go func() {
					if err := a.searchMusic(searchText); err != nil {
						a.application.QueueUpdateDraw(func() {
							a.statusBar.SetText("[red]Search failed: " + err.Error())
						})
					}
				}()
			}
			// 移除悬浮框，恢复主界面
			a.application.SetRoot(a.rootFlex, true)
		} else if key == tcell.KeyEsc {
			// 取消搜索，移除悬浮框
			a.application.SetRoot(a.rootFlex, true)
		}
	})

	// 使用 Pages 来实现悬浮效果
	pages := tview.NewPages().
		AddPage("main", a.rootFlex, true, true).
		AddPage("search", modalFlex, true, false)

	// 显示搜索页面
	// 显示搜索页面前设置搜索标志
	a.searchMux.Lock()
	a.isSearching = true
	a.searchMux.Unlock()

	pages.ShowPage("search")
	return searchInput, pages
}
func (a *Application) SetVolume(addFlag bool) {
	if a.mpvInstance != nil && a.mpvInstance.Mpv != nil {
		go func() {
			// 获取当前音量
			currentVol, err := a.mpvInstance.GetProperty("volume", mpv.FORMAT_DOUBLE)
			if err == nil {
				newVol := currentVol.(float64)
				if addFlag {
					newVol += 5.0 // 增加5%
					if newVol > 100 {
						newVol = 100
					}
				} else {
					newVol -= 5.0
					if newVol < 0 {
						newVol = 0
					}
				}

				a.mpvInstance.SetProperty("volume", mpv.FORMAT_DOUBLE, newVol)
			}
		}()
	}
}

func (a *Application) renderSongTable() {
	// 清除现有行，但保留表头
	for i := a.songTable.GetRowCount() - 1; i > 0; i-- {
		a.songTable.RemoveRow(i)
	}

	pageData := a.getCurrentPageData()

	// 保存匹配的行索引
	matchingRows := make([]int, 0)

	for i, song := range pageData {
		row := i + 1

		rowStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDefault)

		trackCell := tview.NewTableCell(fmt.Sprintf("%d:", row)).
			SetStyle(rowStyle.Foreground(tcell.ColorLightGreen)).
			SetAlign(tview.AlignRight)

		titleCell := tview.NewTableCell(song.Title).
			SetStyle(rowStyle.Foreground(tcell.ColorWhite)).
			SetExpansion(1)

		artistCell := tview.NewTableCell(song.Artist).
			SetStyle(rowStyle.Foreground(tcell.ColorGray)).
			SetMaxWidth(25)

		albumCell := tview.NewTableCell(song.Album).
			SetStyle(rowStyle.Foreground(tcell.ColorGray)).
			SetMaxWidth(25)

		durationCell := tview.NewTableCell(formatDuration(song.Duration)).
			SetStyle(rowStyle.Foreground(tcell.ColorGray)).
			SetAlign(tview.AlignRight)

		a.songTable.SetCell(row, 0, trackCell)
		a.songTable.SetCell(row, 1, titleCell)
		a.songTable.SetCell(row, 2, artistCell)
		a.songTable.SetCell(row, 3, albumCell)
		a.songTable.SetCell(row, 4, durationCell)
		// 如果是搜索结果，记录匹配的行
		if len(a.totalSongs) == 1 && reflect.DeepEqual(&a.totalSongs[0], &song) {
			matchingRows = append(matchingRows, row)
		}
	}

	a.songTable.SetSelectedStyle(tcell.StyleDefault.
		Background(tcell.ColorDarkGreen).
		Foreground(tcell.ColorWhite))

	a.songTable.ScrollToBeginning()

	// 如果有匹配的行，则定位到第一行匹配的行
	if len(matchingRows) > 0 {
		a.songTable.Select(matchingRows[0], 0)
	}
}

func (a *Application) loadMusic() error {
	songs, err := a.subsonicClient.GetPlaylists()
	if err != nil {
		return fmt.Errorf("error get song list: %v", err)
	}

	if !reflect.DeepEqual(a.totalSongs, songs) {
		a.totalSongs = songs
		a.totalPages = (len(a.totalSongs) + a.pageSize - 1) / a.pageSize
		a.application.QueueUpdateDraw(func() {
			a.renderSongTable()
		})
	}
	return nil
}
func (a *Application) searchMusic(name string) error {
	matchingSongs := make([]subsonic.Song, 0)

	for _, song := range a.totalSongs {
		if strings.Contains(strings.ToLower(song.Title), strings.ToLower(name)) ||
			strings.Contains(strings.ToLower(song.Artist), strings.ToLower(name)) ||
			strings.Contains(strings.ToLower(song.Album), strings.ToLower(name)) {
			matchingSongs = append(matchingSongs, song)
		}
	}

	if len(matchingSongs) > 0 {
		a.totalSongs = matchingSongs
		a.totalPages = (len(a.totalSongs) + a.pageSize - 1) / a.pageSize
		a.application.QueueUpdateDraw(func() {
			a.renderSongTable()
		})
		return nil
	}

	// 如果没有找到匹配项，显示提示信息
	a.application.QueueUpdateDraw(func() {
		a.statusBar.SetText(fmt.Sprintf("[yellow]No results found for: %s", name))
	})

	return nil
}

func (a *Application) muteButton() {
	if a.mpvInstance != nil && a.mpvInstance.Mpv != nil {
		go func() {
			currentMute, err := a.mpvInstance.GetProperty("mute", mpv.FORMAT_FLAG)
			if err == nil {
				a.mpvInstance.SetProperty("mute", mpv.FORMAT_FLAG, !currentMute.(bool))
			}
		}()
	}
}

func eventListener(ctx context.Context, m *mpv.Mpv) chan *mpv.Event {
	c := make(chan *mpv.Event)
	go func() {
		defer close(c)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				e := m.WaitEvent(1)
				if e == nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				select {
				case c <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return c
}

func ViperInit() {
	required := []string{
		"server.url",
		"server.username",
		"server.password",
	}
	viper.SetConfigName("config")
	viper.SetConfigType("toml")

	viper.AddConfigPath("$HOME/.config/")
	viper.AddConfigPath(".")

	viper.SetDefault("keys.search", "/")

	if err := viper.ReadInConfig(); err != nil {
		os.Exit(1)
	}

	for _, key := range required {
		if !viper.IsSet(key) {
			os.Exit(1)
		}
	}
}

func main() {
	ViperInit()

	subsonicClient := subsonic.Init(
		viper.GetString("server.url"),
		viper.GetString("server.username"),
		viper.GetString("server.password"),
		"goplayer",
		"1.16.1",
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mpvInstance, err := mpvplayer.CreateMPVInstance()
	if err != nil {
		log.Fatal(err)
	}
	mpvInstance.SetProperty("volume", mpv.FORMAT_DOUBLE, 50.0)
	app := &Application{
		application:    tview.NewApplication(),
		subsonicClient: subsonicClient,
		mpvInstance:    &mpvplayer.Mpvplayer{mpvInstance, eventListener(ctx, mpvInstance), make([]mpvplayer.QueueItem, 0), false},
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("receive exit signal, cleaning resource...")

		if app.mpvInstance != nil && app.mpvInstance.Mpv != nil {
			app.mpvInstance.Command([]string{"quit"})
			app.mpvInstance.TerminateDestroy()
		}

		cancel()
		app.application.Stop()

		go func() {
			time.Sleep(2 * time.Second)
			log.Println("force quit.")
			os.Exit(0)
		}()
	}()

	app.setupPagination()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Println(r)
			}
		}()

		for {
			select {
			case event, ok := <-app.mpvInstance.EventChannel:
				if !ok {
					return
				}
				if event != nil && event.Event_Id == mpv.EVENT_END_FILE {
					app.application.QueueUpdateDraw(func() {
						app.playNextSong()
					})
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go app.updateProgressBar()
	go func() {
		if err := app.loadMusic(); err != nil {
			app.application.QueueUpdateDraw(func() {
				app.statusBar.SetText("[red]load music failed: " + err.Error())
			})
		}
	}()
	app.createHomepage()

	log.Println("start navicli...")
	err = app.application.Run()

	log.Println("program exiting, clear resource...")
	cancel()

	if app.mpvInstance != nil && app.mpvInstance.Mpv != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {

				}
			}()
			app.mpvInstance.Command([]string{"quit"})
			app.mpvInstance.TerminateDestroy()
		}()
	}

	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	log.Println("program exit.")
	os.Exit(0)
}
