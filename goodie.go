package goodie

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/debspencer/html"
	"github.com/go-xorm/xorm"
	"xorm.io/core"
)

var (
	defaultReadTimeout    = 10 * time.Second
	defaultWriteTimeout   = 10 * time.Second
	defaultMaxHeaderBytes = 16 * 1024

	NotFound    = errors.New("Not Found")
	ServerError = errors.New("Internal Server Error")
)

type NewHandler func() Handler

type Server struct {
	Addr           string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxHeaderBytes int

	handlers map[string]AppHandler
	favicon  []byte
	home     string
}

type App struct {
	odie *Server
	name string
	orm  *xorm.Engine
}

type Handler interface {
	render(app *App, w http.ResponseWriter, req *http.Request, handler Handler) // implemented by Odie

	// Init App.  Returns slice of urls showing stack (index -> page1 -> page2)
	// Optional, if []byte is returned, then that data is written
	Init() ([]*html.URL, []byte, error)
	RenderError(err error)
	Action(string) (*html.URL, error) // return a URL to refersh to when complete
	Header([]*html.URL)
	Display()
	Footer([]*html.URL)
}

func Init(addr string, o *Server) *Server {
	if o == nil {
		o = &Server{
			ReadTimeout:    defaultReadTimeout,
			WriteTimeout:   defaultWriteTimeout,
			MaxHeaderBytes: defaultMaxHeaderBytes,
		}
		o.SetHome(os.Getenv("GOODIE_HOME"))
	}
	o.Addr = addr
	o.handlers = make(map[string]AppHandler)
	return o
}

func (s *Server) NewApp(name string) *App {
	return &App{
		odie: s,
		name: name,
	}
}

func (s *Server) AddFavicon(favicon []byte) {
	s.favicon = favicon
}

func (s *Server) SetHome(home string) {
	s.home = home
}

func (s *Server) Path(file string) string {
	return path.Join(s.home, file)
}

func (a *App) SetDb(db string) error {
	db = a.odie.Path(db)
	fmt.Println("SetDB", db)
	orm, err := xorm.NewEngine("sqlite3", db)

	if err != nil {
		return err
	}

	orm.SetColumnMapper(core.SnakeMapper{})
	orm.SetMaxOpenConns(5)
	//	orm.SetLogger(&logger{})
	orm.ShowSQL(true)
	a.orm = orm

	return nil
}

type AppHandler struct {
	handler NewHandler
	app     *App
}

func (a *App) Register(page string, h NewHandler) {
	if len(page) > 0 && !strings.HasPrefix(page, "/") {
		page = "/" + page
	}
	page = "/" + a.name + page
	fmt.Println("Register:", page)
	a.odie.handlers[page] = AppHandler{
		handler: h,
		app:     a,
	}
}

func (a *App) Path(element string) string {
	return a.odie.Path(element)
}

func (o *Server) Run() error {
	s := &http.Server{
		Addr:           o.Addr,
		Handler:        o,
		ReadTimeout:    o.ReadTimeout,
		WriteTimeout:   o.WriteTimeout,
		MaxHeaderBytes: o.MaxHeaderBytes,
	}
	return s.ListenAndServe()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path
	fmt.Println("Request:", path, req.URL.RawQuery)
	appHandler, ok := s.handlers[path]
	if !ok {
		if path == "/favicon.ico" && len(s.favicon) > 0 {
			s.showFavicon(w)
			return
		}

		fmt.Printf("404 = '%s'\n", req.URL.Path)
		w.WriteHeader(404)
		return
	}

	handler := appHandler.handler()
	handler.render(appHandler.app, w, req, handler)
}
func (s *Server) showFavicon(w http.ResponseWriter) {
	w.Header().Add("Content-type", "image/x-icon")
	w.Write(s.favicon)
}

type Odie struct {
	Request    *http.Request
	Response   http.ResponseWriter
	Doc        *html.Document
	Body       *html.BodyElement
	Url        *html.URL
	Orm        *xorm.Engine
	Path       string // Path to applicatio's base directory
	defaultUrl *html.URL
}

// Render will create an HTML docuement and render the page
func (odie *Odie) render(app *App, w http.ResponseWriter, req *http.Request, handler Handler) {
	odie.Request = req
	odie.Response = w

	w.Header().Add("Expires", "Sat, Jan 1 2000 00:00:00 GMT")
	w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")

	req.ParseForm()
	odie.Url = html.NewURL(req.URL, req.Form)

	odie.Orm = app.orm
	odie.Path = app.Path(app.name)

	// create the HTML doc, but don't add a body to it yet
	odie.Doc = html.NewDocument()
	odie.Doc.AddCSS(html.CSS(default_css))

	// call handler's init method.  It will return the base named.
	urls, data, err := handler.Init()
	if err != nil {
		odie.RenderError(err)
		return
	}

	if data != nil {
		odie.Response.Write(data)
		return
	}

	// urls will be a stacked list of urls for the header.  The last url will be the current page
	var topurl *html.URL
	if len(urls) > 0 {
		topurl = urls[len(urls)-1]
	}

	odie.defaultUrl = topurl
	if odie.defaultUrl == nil {
		odie.defaultUrl = odie.DefaultURL()
	}

	// if there is an action query string, the perform the action
	action := odie.Url.GetQuery("action")
	if len(action) > 0 {
		refreshUrl, err := handler.Action(action)

		if err != nil {
			odie.RenderError(err)
			return
		}

		// if refresh, then we will want to reload the page, so a ^R refresh doesn't repeat the action
		if refreshUrl != nil {
			refreshUrl.DelQuery("action") // remove action so we don't go into an infinite loop

			odie.Doc.Head().Add(html.MetaRefresh(0, refreshUrl.Link()))
			odie.Doc.Render(odie.Response)
			return
		}
	}

	// create html body now we are ready to render
	odie.Body = odie.Doc.Body()
	odie.Body.AddClassName("goodiebody")

	if err != nil {
		if err == NotFound {
			w.WriteHeader(404)
			return
		}
		if err == ServerError {
			w.WriteHeader(500)
			return
		}
	}

	// Set a default title if init did not do so
	title := odie.Doc.Head().GetTitle()
	if len(title) == 0 && topurl != nil {
		odie.Doc.Head().AddTitle(topurl.Name)
	}

	handler.Header(urls)
	handler.Display()
	handler.Footer(urls)

	odie.Doc.Render(odie.Response)
}

func (odie *Odie) SetContentType(mimeType html.MimeType) {
	odie.Response.Header().Add("Content-type", mimeType.Mime)
}

func (odie *Odie) DefaultURL() *html.URL {
	if odie.defaultUrl != nil {
		return odie.defaultUrl
	}
	u := html.NewURL(odie.Request.URL, nil)
	u.Name = odie.Request.URL.Path
	u.Query = nil
	u.Anchor = ""
	return u
}
func (odie *Odie) HomeURL() *html.URL {
	u := html.NewURL(odie.Request.URL, nil)
	u.Name = "Home"
	u.App = ""
	u.Page = "/"
	u.Query = nil
	u.Anchor = ""
	//	fmt.Printf("%#v\n", u)
	return u
}

func (odie *Odie) NewForm(action string) *html.FormElement {
	l := html.NewLink(odie.Request.URL.Path)
	f := html.Form(l)
	/*
		qs := odie.Request.URL.Query()
		for k, vs := range qs {
			var v string
			if len(vs) > 0 {
				v = vs[0]
			}
			f.Add(html.Hidden(k, v))
		}
	*/
	if len(action) > 0 {
		f.Add(html.Hidden("action", action))
	}
	return f
}

// ShowHeader will return the inner div and outer div
func (odie *Odie) ShowHeader(which string, urls []*html.URL) (*html.DivElement, *html.DivElement) {
	outerDiv := html.Div()
	outerDiv.AddClassName("goodie" + which)

	innerDiv := urlStack(urls)

	h := html.Heading(3, innerDiv)
	outerDiv.Add(h)
	odie.Body.Add(outerDiv)
	return outerDiv, innerDiv
}

func urlStack(urls []*html.URL) *html.DivElement {
	div := html.Div()
	for i, url := range urls {
		// last one
		if i == len(urls)-1 {
			div.Add(html.Text(url.Name))
		} else {
			div.Add(url)
			div.Add(html.Text(">"))
		}
	}
	return div
}

// Override methods

// Init before page is displayed.   No access to HTML at this time
func (odie *Odie) Init() []*html.URL {
	return []*html.URL{odie.DefaultURL()}
}

// RednerError is called anytime a fatal error is encountered
func (odie *Odie) RenderError(err error) {
	odie.Body = odie.Doc.Body()
	odie.Body.AddClassName("goodieerror")
	odie.Body.Add(html.Text(err.Error()))

	odie.Doc.Render(odie.Response)
}

// Action will perform an action before the page loads.
// return value of url of refresh page without action query string.  The allows for db updates and a page reload would not do a double action
// error value of nil will continue on to render.  A page reload will recall action
// Controlled by presense of action= query string
func (odie *Odie) Action(action string) (*html.URL, error) {
	return nil, nil
}

// Header will show the page header
func (odie *Odie) Header(urls []*html.URL) {
	odie.ShowHeader("header", urls)
}

// Display will show render page between header and footer
func (odie *Odie) Display() {
}

// Footer will show the page footer
func (odie *Odie) Footer(urls []*html.URL) {
	odie.ShowHeader("footer", urls)
}

func (odie *Odie) LoadFromQuery(iface interface{}) error {
	rValue := reflect.ValueOf(iface)

	fmt.Printf("LoadFromQuery %+v\n", iface)

	switch rValue.Kind() {
	case reflect.Ptr:
		if rValue.IsNil() {
			return fmt.Errorf("FromUrl: ptr is nil")
		}
		rValue = rValue.Elem()
	default:
		return fmt.Errorf("FromUrl: %T is not ptr", iface)
	}

	switch rValue.Kind() {
	case reflect.Struct:
		for i := 0; i != rValue.NumField(); i++ {
			fieldValue := rValue.Field(i)

			if !fieldValue.CanInterface() {
				continue
			}

			field := rValue.Type().Field(i)
			key := strings.ToLower(field.Name)

			var q string
			for _, key := range []string{strings.ToLower(field.Name), underscoreKey(field.Name)} {
				q = odie.Url.GetQuery(key)
				fmt.Printf("%s = '%s'\n", key, q)
				if len(q) > 0 {
					break
				}
			}
			if len(q) == 0 {
				continue
			}

			t := field.Type.Kind()
			switch t {
			case reflect.String:
				fieldValue.SetString(q)
			case reflect.Int64, reflect.Int:
				n, err := strconv.ParseInt(q, 10, 64)
				if err != nil {
					return fmt.Errorf("Not an int: %s = %s (%s)", key, q, err.Error())
				}
				fieldValue.SetInt(n)
			case reflect.Struct:
				iface := rValue.Field(i).Interface()
				switch iface.(type) {
				case sql.NullInt64:
					n, err := strconv.ParseInt(q, 10, 64)
					if err != nil {
						return fmt.Errorf("Not an int: %s = %s (%s)", key, q, err.Error())
					}
					si64 := sql.NullInt64{
						Valid: true,
						Int64: n,
					}
					v := reflect.ValueOf(si64)
					fieldValue.Set(v)
				default:
					fmt.Println("Unsuported struct type %T %T for key: %s", field, iface, key)
					return fmt.Errorf("Unsuported type %T %T for key: %s", field, iface, key)
				}
			default:
				fmt.Println("Unsuported type %t for key: %s", field, key)
				return fmt.Errorf("Unsuported type %T for key: %s", field, key)
			}

		}
	case reflect.Slice, reflect.Array:
		return fmt.Errorf("FromUrl: Can not decode %T", iface)
	case reflect.Map:
		return fmt.Errorf("FromUrl: Can not decode %T", iface)
	default:
		return fmt.Errorf("FromUrl: Can not decode %T", iface)
	}
	return nil
}

func (odie *Odie) DbInsert(v interface{}) error {
	if odie.Orm == nil {
		return fmt.Errorf("DB not configured")
	}

	affected, err := odie.Orm.Insert(v)
	return expect("inserted", affected, 1, err, v)
}
func (odie *Odie) DbGet(id int64, v interface{}) error {
	if odie.Orm == nil {
		return fmt.Errorf("DB not configured")
	}

	// has, err := odie.Orm.Where(Eq{"id": id}).Get(v)
	has, err := odie.Orm.ID(id).Get(v)
	return hasRecords(has, err, id)
}
func (odie *Odie) DbDelete(v interface{}) error {
	if odie.Orm == nil {
		return fmt.Errorf("DB not configured")
	}

	affected, err := odie.Orm.Delete(v)
	return expect("deleted", affected, 1, err, v)
}
func (odie *Odie) DbUpdate(id int64, v interface{}) error {
	if odie.Orm == nil {
		return fmt.Errorf("DB not configured")
	}

	affected, err := odie.Orm.ID(id).Update(v)
	return expect("updated", affected, 1, err, v)
}

func (odie *Odie) GetAll(v interface{}) error {
	if odie.Orm == nil {
		return fmt.Errorf("DB not configured")
	}

	return odie.Orm.Find(v)
}

func (odie *Odie) GetOrder(v interface{}, order string) error {
	if odie.Orm == nil {
		return fmt.Errorf("DB not configured")
	}

	return odie.Orm.OrderBy(order).Find(v)
}

func expect(what string, affected int64, expected int64, err error, i interface{}) error {
	if err != nil {
		return err
	}
	if affected != expected {
		return fmt.Errorf("Expected %d rows %s, Got %d for record %+v", expected, what, affected, i)
	}
	return nil
}
func hasRecords(has bool, err error, id int64) error {
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf("No Records Found for id %d", id)
	}
	return nil
}

func underscoreKey(key string) string {
	runes := make([]rune, 0, len(key)*2)
	for i, r := range key {
		isUpper := 'A' <= r && r <= 'Z'
		if isUpper {
			if i > 0 {
				runes = append(runes, '_')
			}
			r -= ('A' - 'a')
		}
		runes = append(runes, r)
	}

	return string(runes)
}
