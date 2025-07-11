// Copyright 2024 The Hugo Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hugolib

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bep/logg"
	"github.com/gobuffalo/flect"
	"github.com/gohugoio/hugo/langs"
	"github.com/gohugoio/hugo/markup/converter"
	xmaps "golang.org/x/exp/maps"

	"github.com/gohugoio/hugo/source"

	"github.com/gohugoio/hugo/common/hashing"
	"github.com/gohugoio/hugo/common/hugo"
	"github.com/gohugoio/hugo/common/loggers"
	"github.com/gohugoio/hugo/common/maps"
	"github.com/gohugoio/hugo/common/paths"
	"github.com/gohugoio/hugo/config"
	"github.com/gohugoio/hugo/helpers"

	"github.com/gohugoio/hugo/output"
	"github.com/gohugoio/hugo/resources/kinds"
	"github.com/gohugoio/hugo/resources/page"
	"github.com/gohugoio/hugo/resources/page/pagemeta"
	"github.com/gohugoio/hugo/resources/resource"
	"github.com/spf13/cast"
)

var cjkRe = regexp.MustCompile(`\p{Han}|\p{Hangul}|\p{Hiragana}|\p{Katakana}`)

type pageMeta struct {
	term     string // Set for kind == KindTerm.
	singular string // Set for kind == KindTerm and kind == KindTaxonomy.

	resource.Staler
	*pageMetaParams

	// Set for standalone pages, e.g. robotsTXT.
	standaloneOutputFormat output.Format

	resourcePath string // Set for bundled pages; path relative to its bundle root.
	bundled      bool   // Set if this page is bundled inside another.

	pathInfo *paths.Path // Always set. This the canonical path to the Page.
	f        *source.File

	content *cachedContent // The source and the parsed page content.

	s *Site // The site this page belongs to.
}

// Prepare for a rebuild of the data passed in from front matter.
func (m *pageMeta) setMetaPostPrepareRebuild() {
	params := xmaps.Clone(m.paramsOriginal)
	m.pageMetaParams.pageConfig = pagemeta.ClonePageConfigForRebuild(m.pageMetaParams.pageConfig, params)
}

type pageMetaParams struct {
	setMetaPostCount          int
	setMetaPostCascadeChanged bool

	pageConfig *pagemeta.PageConfig

	// These are only set in watch mode.
	datesOriginal   pagemeta.Dates
	paramsOriginal  map[string]any                                                // contains the original params as defined in the front matter.
	cascadeOriginal *maps.Ordered[page.PageMatcher, page.PageMatcherParamsConfig] // contains the original cascade as defined in the front matter.
}

func (m *pageMetaParams) init(preserveOriginal bool) {
	if preserveOriginal {
		if m.pageConfig.IsFromContentAdapter {
			m.paramsOriginal = xmaps.Clone(m.pageConfig.ContentAdapterData)
		} else {
			m.paramsOriginal = xmaps.Clone(m.pageConfig.Params)
		}
		m.cascadeOriginal = m.pageConfig.CascadeCompiled.Clone()
	}
}

func (p *pageMeta) Aliases() []string {
	return p.pageConfig.Aliases
}

func (p *pageMeta) BundleType() string {
	switch p.pathInfo.Type() {
	case paths.TypeLeaf:
		return "leaf"
	case paths.TypeBranch:
		return "branch"
	default:
		return ""
	}
}

func (p *pageMeta) Date() time.Time {
	return p.pageConfig.Dates.Date
}

func (p *pageMeta) PublishDate() time.Time {
	return p.pageConfig.Dates.PublishDate
}

func (p *pageMeta) Lastmod() time.Time {
	return p.pageConfig.Dates.Lastmod
}

func (p *pageMeta) ExpiryDate() time.Time {
	return p.pageConfig.Dates.ExpiryDate
}

func (p *pageMeta) Description() string {
	return p.pageConfig.Description
}

func (p *pageMeta) Lang() string {
	return p.s.Lang()
}

func (p *pageMeta) Draft() bool {
	return p.pageConfig.Draft
}

func (p *pageMeta) File() *source.File {
	return p.f
}

func (p *pageMeta) IsHome() bool {
	return p.Kind() == kinds.KindHome
}

func (p *pageMeta) Keywords() []string {
	return p.pageConfig.Keywords
}

func (p *pageMeta) Kind() string {
	return p.pageConfig.Kind
}

func (p *pageMeta) Layout() string {
	return p.pageConfig.Layout
}

func (p *pageMeta) LinkTitle() string {
	if p.pageConfig.LinkTitle != "" {
		return p.pageConfig.LinkTitle
	}

	return p.Title()
}

func (p *pageMeta) Name() string {
	if p.resourcePath != "" {
		return p.resourcePath
	}
	if p.pageConfig.Kind == kinds.KindTerm {
		return p.pathInfo.Unnormalized().BaseNameNoIdentifier()
	}
	return p.Title()
}

func (p *pageMeta) IsNode() bool {
	return !p.IsPage()
}

func (p *pageMeta) IsPage() bool {
	return p.Kind() == kinds.KindPage
}

// Param is a convenience method to do lookups in Page's and Site's Params map,
// in that order.
//
// This method is also implemented on SiteInfo.
// TODO(bep) interface
func (p *pageMeta) Param(key any) (any, error) {
	return resource.Param(p, p.s.Params(), key)
}

func (p *pageMeta) Params() maps.Params {
	return p.pageConfig.Params
}

func (p *pageMeta) Path() string {
	return p.pathInfo.Base()
}

func (p *pageMeta) PathInfo() *paths.Path {
	return p.pathInfo
}

func (p *pageMeta) IsSection() bool {
	return p.Kind() == kinds.KindSection
}

func (p *pageMeta) Section() string {
	return p.pathInfo.Section()
}

func (p *pageMeta) Sitemap() config.SitemapConfig {
	return p.pageConfig.Sitemap
}

func (p *pageMeta) Title() string {
	return p.pageConfig.Title
}

const defaultContentType = "page"

func (p *pageMeta) Type() string {
	if p.pageConfig.Type != "" {
		return p.pageConfig.Type
	}

	if sect := p.Section(); sect != "" {
		return sect
	}

	return defaultContentType
}

func (p *pageMeta) Weight() int {
	return p.pageConfig.Weight
}

func (p *pageMeta) setMetaPre(pi *contentParseInfo, logger loggers.Logger, conf config.AllProvider) error {
	frontmatter := pi.frontMatter

	if frontmatter != nil {
		pcfg := p.pageConfig
		// Needed for case insensitive fetching of params values
		maps.PrepareParams(frontmatter)
		pcfg.Params = frontmatter
		// Check for any cascade define on itself.
		if cv, found := frontmatter["cascade"]; found {
			var err error
			cascade, err := page.DecodeCascade(logger, true, cv)
			if err != nil {
				return err
			}
			pcfg.CascadeCompiled = cascade
		}

		// Look for path, lang and kind, all of which values we need early on.
		if v, found := frontmatter["path"]; found {
			pcfg.Path = paths.ToSlashPreserveLeading(cast.ToString(v))
			pcfg.Params["path"] = pcfg.Path
		}
		if v, found := frontmatter["lang"]; found {
			lang := strings.ToLower(cast.ToString(v))
			if _, ok := conf.PathParser().LanguageIndex[lang]; ok {
				pcfg.Lang = lang
				pcfg.Params["lang"] = pcfg.Lang
			}
		}
		if v, found := frontmatter["kind"]; found {
			s := cast.ToString(v)
			if s != "" {
				pcfg.Kind = kinds.GetKindMain(s)
				if pcfg.Kind == "" {
					return fmt.Errorf("unknown kind %q in front matter", s)
				}
				pcfg.Params["kind"] = pcfg.Kind
			}
		}
	} else if p.pageMetaParams.pageConfig.Params == nil {
		p.pageConfig.Params = make(maps.Params)
	}

	p.pageMetaParams.init(conf.Watching())

	return nil
}

func (ps *pageState) setMetaPost(cascade *maps.Ordered[page.PageMatcher, page.PageMatcherParamsConfig]) error {
	ps.m.setMetaPostCount++
	var cascadeHashPre uint64
	if ps.m.setMetaPostCount > 1 {
		cascadeHashPre = hashing.HashUint64(ps.m.pageConfig.CascadeCompiled)
		ps.m.pageConfig.CascadeCompiled = ps.m.cascadeOriginal.Clone()

	}

	// Apply cascades first so they can be overridden later.
	if cascade != nil {
		if ps.m.pageConfig.CascadeCompiled != nil {
			cascade.Range(func(k page.PageMatcher, v page.PageMatcherParamsConfig) bool {
				vv, found := ps.m.pageConfig.CascadeCompiled.Get(k)
				if !found {
					ps.m.pageConfig.CascadeCompiled.Set(k, v)
				} else {
					// Merge
					for ck, cv := range v.Params {
						if _, found := vv.Params[ck]; !found {
							vv.Params[ck] = cv
						}
					}
					for ck, cv := range v.Fields {
						if _, found := vv.Fields[ck]; !found {
							vv.Fields[ck] = cv
						}
					}
				}
				return true
			})
			cascade = ps.m.pageConfig.CascadeCompiled
		} else {
			ps.m.pageConfig.CascadeCompiled = cascade
		}
	}

	if cascade == nil {
		cascade = ps.m.pageConfig.CascadeCompiled
	}

	if ps.m.setMetaPostCount > 1 {
		ps.m.setMetaPostCascadeChanged = cascadeHashPre != hashing.HashUint64(ps.m.pageConfig.CascadeCompiled)
		if !ps.m.setMetaPostCascadeChanged {

			// No changes, restore any value that may be changed by aggregation.
			ps.m.pageConfig.Dates = ps.m.datesOriginal
			return nil
		}
		ps.m.setMetaPostPrepareRebuild()

	}

	// Cascade is also applied to itself.
	var err error
	cascade.Range(func(k page.PageMatcher, v page.PageMatcherParamsConfig) bool {
		if !k.Matches(ps) {
			return true
		}
		for kk, vv := range v.Params {
			if _, found := ps.m.pageConfig.Params[kk]; !found {
				ps.m.pageConfig.Params[kk] = vv
			}
		}

		for kk, vv := range v.Fields {
			if ps.m.pageConfig.IsFromContentAdapter {
				if _, found := ps.m.pageConfig.ContentAdapterData[kk]; !found {
					ps.m.pageConfig.ContentAdapterData[kk] = vv
				}
			} else {
				if _, found := ps.m.pageConfig.Params[kk]; !found {
					ps.m.pageConfig.Params[kk] = vv
				}
			}
		}
		return true
	})

	if err != nil {
		return err
	}

	if err := ps.setMetaPostParams(); err != nil {
		return err
	}

	if err := ps.m.applyDefaultValues(); err != nil {
		return err
	}

	// Store away any original values that may be changed from aggregation.
	ps.m.datesOriginal = ps.m.pageConfig.Dates

	return nil
}

func (p *pageState) setMetaPostParams() error {
	pm := p.m
	var mtime time.Time
	var contentBaseName string
	var ext string
	var isContentAdapter bool
	if p.File() != nil {
		isContentAdapter = p.File().IsContentAdapter()
		contentBaseName = p.File().ContentBaseName()
		if p.File().FileInfo() != nil {
			mtime = p.File().FileInfo().ModTime()
		}
		if !isContentAdapter {
			ext = p.File().Ext()
		}
	}

	var gitAuthorDate time.Time
	if p.gitInfo != nil {
		gitAuthorDate = p.gitInfo.AuthorDate
	}

	descriptor := &pagemeta.FrontMatterDescriptor{
		PageConfig:    pm.pageConfig,
		BaseFilename:  contentBaseName,
		ModTime:       mtime,
		GitAuthorDate: gitAuthorDate,
		Location:      langs.GetLocation(pm.s.Language()),
		PathOrTitle:   p.pathOrTitle(),
	}

	if isContentAdapter {
		if err := pm.pageConfig.Compile(ext, p.s.Log, p.s.conf.OutputFormats.Config, p.s.conf.MediaTypes.Config); err != nil {
			return err
		}
	}

	// Handle the date separately
	// TODO(bep) we need to "do more" in this area so this can be split up and
	// more easily tested without the Page, but the coupling is strong.
	err := pm.s.frontmatterHandler.HandleDates(descriptor)
	if err != nil {
		p.s.Log.Errorf("Failed to handle dates for page %q: %s", p.pathOrTitle(), err)
	}

	if isContentAdapter {
		// Done.
		return nil
	}

	var buildConfig any
	var isNewBuildKeyword bool
	if v, ok := pm.pageConfig.Params["_build"]; ok {
		hugo.Deprecate("The \"_build\" front matter key", "Use \"build\" instead. See https://gohugo.io/content-management/build-options.", "0.145.0")
		buildConfig = v
	} else {
		buildConfig = pm.pageConfig.Params["build"]
		isNewBuildKeyword = true
	}
	pm.pageConfig.Build, err = pagemeta.DecodeBuildConfig(buildConfig)
	if err != nil {
		var msgDetail string
		if isNewBuildKeyword {
			msgDetail = `. We renamed the _build keyword to build in Hugo 0.123.0. We recommend putting user defined params in the params section, e.g.:
---
title: "My Title"
params:
  build: "My Build"
---
´

`
		}
		return fmt.Errorf("failed to decode build config in front matter: %s%s", err, msgDetail)
	}

	var sitemapSet bool

	pcfg := pm.pageConfig
	params := pcfg.Params
	if params == nil {
		panic("params not set for " + p.Title())
	}

	var draft, published, isCJKLanguage *bool
	var userParams map[string]any
	for k, v := range pcfg.Params {
		loki := strings.ToLower(k)

		if loki == "params" {
			vv, err := maps.ToStringMapE(v)
			if err != nil {
				return err
			}
			userParams = vv
			delete(pcfg.Params, k)
			continue
		}

		if loki == "published" { // Intentionally undocumented
			vv, err := cast.ToBoolE(v)
			if err == nil {
				published = &vv
			}
			// published may also be a date
			continue
		}

		if pm.s.frontmatterHandler.IsDateKey(loki) {
			continue
		}

		if loki == "path" || loki == "kind" || loki == "lang" {
			// See issue 12484.
			hugo.DeprecateLevelMin(loki+" in front matter", "", "v0.144.0", logg.LevelWarn)
		}

		switch loki {
		case "title":
			pcfg.Title = cast.ToString(v)
			params[loki] = pcfg.Title
		case "linktitle":
			pcfg.LinkTitle = cast.ToString(v)
			params[loki] = pcfg.LinkTitle
		case "summary":
			pcfg.Summary = cast.ToString(v)
			params[loki] = pcfg.Summary
		case "description":
			pcfg.Description = cast.ToString(v)
			params[loki] = pcfg.Description
		case "slug":
			// Don't start or end with a -
			pcfg.Slug = strings.Trim(cast.ToString(v), "-")
			params[loki] = pm.Slug()
		case "url":
			url := cast.ToString(v)
			if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
				return fmt.Errorf("URLs with protocol (http*) not supported: %q. In page %q", url, p.pathOrTitle())
			}
			pcfg.URL = url
			params[loki] = url
		case "type":
			pcfg.Type = cast.ToString(v)
			params[loki] = pcfg.Type
		case "keywords":
			pcfg.Keywords = cast.ToStringSlice(v)
			params[loki] = pcfg.Keywords
		case "headless":
			// Legacy setting for leaf bundles.
			// This is since Hugo 0.63 handled in a more general way for all
			// pages.
			isHeadless := cast.ToBool(v)
			params[loki] = isHeadless
			if isHeadless {
				pm.pageConfig.Build.List = pagemeta.Never
				pm.pageConfig.Build.Render = pagemeta.Never
			}
		case "outputs":
			o := cast.ToStringSlice(v)
			// lower case names:
			for i, s := range o {
				o[i] = strings.ToLower(s)
			}
			pm.pageConfig.Outputs = o
		case "draft":
			draft = new(bool)
			*draft = cast.ToBool(v)
		case "layout":
			pcfg.Layout = cast.ToString(v)
			params[loki] = pcfg.Layout
		case "markup":
			pcfg.Content.Markup = cast.ToString(v)
			params[loki] = pcfg.Content.Markup
		case "weight":
			pcfg.Weight = cast.ToInt(v)
			params[loki] = pcfg.Weight
		case "aliases":
			pcfg.Aliases = cast.ToStringSlice(v)
			for i, alias := range pcfg.Aliases {
				if strings.HasPrefix(alias, "http://") || strings.HasPrefix(alias, "https://") {
					return fmt.Errorf("http* aliases not supported: %q", alias)
				}
				pcfg.Aliases[i] = filepath.ToSlash(alias)
			}
			params[loki] = pcfg.Aliases
		case "sitemap":
			pcfg.Sitemap, err = config.DecodeSitemap(p.s.conf.Sitemap, maps.ToStringMap(v))
			if err != nil {
				return fmt.Errorf("failed to decode sitemap config in front matter: %s", err)
			}
			sitemapSet = true
		case "iscjklanguage":
			isCJKLanguage = new(bool)
			*isCJKLanguage = cast.ToBool(v)
		case "translationkey":
			pcfg.TranslationKey = cast.ToString(v)
			params[loki] = pcfg.TranslationKey
		case "resources":
			var resources []map[string]any
			handled := true

			switch vv := v.(type) {
			case []map[any]any:
				for _, vvv := range vv {
					resources = append(resources, maps.ToStringMap(vvv))
				}
			case []map[string]any:
				resources = append(resources, vv...)
			case []any:
				for _, vvv := range vv {
					switch vvvv := vvv.(type) {
					case map[any]any:
						resources = append(resources, maps.ToStringMap(vvvv))
					case map[string]any:
						resources = append(resources, vvvv)
					}
				}
			default:
				handled = false
			}

			if handled {
				pcfg.ResourcesMeta = resources
				break
			}
			fallthrough
		default:
			// If not one of the explicit values, store in Params
			switch vv := v.(type) {
			case []any:
				if len(vv) > 0 {
					allStrings := true
					for _, vvv := range vv {
						if _, ok := vvv.(string); !ok {
							allStrings = false
							break
						}
					}
					if allStrings {
						// We need tags, keywords etc. to be []string, not []interface{}.
						a := make([]string, len(vv))
						for i, u := range vv {
							a[i] = cast.ToString(u)
						}
						params[loki] = a
					} else {
						params[loki] = vv
					}
				} else {
					params[loki] = []string{}
				}

			default:
				params[loki] = vv
			}
		}
	}

	for k, v := range userParams {
		params[strings.ToLower(k)] = v
	}

	if !sitemapSet {
		pcfg.Sitemap = p.s.conf.Sitemap
	}

	if draft != nil && published != nil {
		pcfg.Draft = *draft
		p.m.s.Log.Warnf("page %q has both draft and published settings in its frontmatter. Using draft.", p.File().Filename())
	} else if draft != nil {
		pcfg.Draft = *draft
	} else if published != nil {
		pcfg.Draft = !*published
	}
	params["draft"] = pcfg.Draft

	if isCJKLanguage != nil {
		pcfg.IsCJKLanguage = *isCJKLanguage
	} else if p.s.conf.HasCJKLanguage && p.m.content.pi.openSource != nil {
		if cjkRe.Match(p.m.content.mustSource()) {
			pcfg.IsCJKLanguage = true
		} else {
			pcfg.IsCJKLanguage = false
		}
	}

	params["iscjklanguage"] = pcfg.IsCJKLanguage

	if err := pcfg.Init(false); err != nil {
		return err
	}

	if err := pcfg.Compile(ext, p.s.Log, p.s.conf.OutputFormats.Config, p.s.conf.MediaTypes.Config); err != nil {
		return err
	}

	return nil
}

// shouldList returns whether this page should be included in the list of pages.
// global indicates site.Pages etc.
func (p *pageMeta) shouldList(global bool) bool {
	if p.isStandalone() {
		// Never list 404, sitemap and similar.
		return false
	}

	switch p.pageConfig.Build.List {
	case pagemeta.Always:
		return true
	case pagemeta.Never:
		return false
	case pagemeta.ListLocally:
		return !global
	}
	return false
}

func (p *pageMeta) shouldListAny() bool {
	return p.shouldList(true) || p.shouldList(false)
}

func (p *pageMeta) isStandalone() bool {
	return !p.standaloneOutputFormat.IsZero()
}

func (p *pageMeta) shouldBeCheckedForMenuDefinitions() bool {
	if !p.shouldList(false) {
		return false
	}

	return p.pageConfig.Kind == kinds.KindHome || p.pageConfig.Kind == kinds.KindSection || p.pageConfig.Kind == kinds.KindPage
}

func (p *pageMeta) noRender() bool {
	return p.pageConfig.Build.Render != pagemeta.Always
}

func (p *pageMeta) noLink() bool {
	return p.pageConfig.Build.Render == pagemeta.Never
}

func (p *pageMeta) applyDefaultValues() error {
	if p.pageConfig.Build.IsZero() {
		p.pageConfig.Build, _ = pagemeta.DecodeBuildConfig(nil)
	}

	if !p.s.conf.IsKindEnabled(p.Kind()) {
		(&p.pageConfig.Build).Disable()
	}

	if p.pageConfig.Content.Markup == "" {
		if p.File() != nil {
			// Fall back to file extension
			p.pageConfig.Content.Markup = p.s.ContentSpec.ResolveMarkup(p.File().Ext())
		}
		if p.pageConfig.Content.Markup == "" {
			p.pageConfig.Content.Markup = "markdown"
		}
	}

	if p.pageConfig.Title == "" && p.f == nil {
		switch p.Kind() {
		case kinds.KindHome:
			p.pageConfig.Title = p.s.Title()
		case kinds.KindSection:
			sectionName := p.pathInfo.Unnormalized().BaseNameNoIdentifier()
			if p.s.conf.PluralizeListTitles {
				sectionName = flect.Pluralize(sectionName)
			}
			if p.s.conf.CapitalizeListTitles {
				sectionName = p.s.conf.C.CreateTitle(sectionName)
			}
			p.pageConfig.Title = sectionName
		case kinds.KindTerm:
			if p.term != "" {
				if p.s.conf.CapitalizeListTitles {
					p.pageConfig.Title = p.s.conf.C.CreateTitle(p.term)
				} else {
					p.pageConfig.Title = p.term
				}
			} else {
				panic("term not set")
			}
		case kinds.KindTaxonomy:
			if p.s.conf.CapitalizeListTitles {
				p.pageConfig.Title = strings.Replace(p.s.conf.C.CreateTitle(p.pathInfo.Unnormalized().BaseNameNoIdentifier()), "-", " ", -1)
			} else {
				p.pageConfig.Title = strings.Replace(p.pathInfo.Unnormalized().BaseNameNoIdentifier(), "-", " ", -1)
			}
		case kinds.KindStatus404:
			p.pageConfig.Title = "404 Page not found"
		}
	}

	return nil
}

func (p *pageMeta) newContentConverter(ps *pageState, markup string) (converter.Converter, error) {
	if ps == nil {
		panic("no Page provided")
	}
	cp := p.s.ContentSpec.Converters.Get(markup)
	if cp == nil {
		return converter.NopConverter, fmt.Errorf("no content renderer found for markup %q, page: %s", markup, ps.getPageInfoForError())
	}

	var id string
	var filename string
	var path string
	if p.f != nil {
		id = p.f.UniqueID()
		filename = p.f.Filename()
		path = p.f.Path()
	} else {
		path = p.Path()
	}

	doc := newPageForRenderHook(ps)

	documentLookup := func(id uint64) any {
		if id == ps.pid {
			// This prevents infinite recursion in some cases.
			return doc
		}
		if v, ok := ps.pageOutput.pco.otherOutputs.Get(id); ok {
			return v.po.p
		}
		return nil
	}

	cpp, err := cp.New(
		converter.DocumentContext{
			Document:       doc,
			DocumentLookup: documentLookup,
			DocumentID:     id,
			DocumentName:   path,
			Filename:       filename,
		},
	)
	if err != nil {
		return converter.NopConverter, err
	}

	return cpp, nil
}

// The output formats this page will be rendered to.
func (m *pageMeta) outputFormats() output.Formats {
	if len(m.pageConfig.ConfiguredOutputFormats) > 0 {
		return m.pageConfig.ConfiguredOutputFormats
	}
	return m.s.conf.C.KindOutputFormats[m.Kind()]
}

func (p *pageMeta) Slug() string {
	return p.pageConfig.Slug
}

func getParam(m resource.ResourceParamsProvider, key string, stringToLower bool) any {
	v := m.Params()[strings.ToLower(key)]

	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case bool:
		return val
	case string:
		if stringToLower {
			return strings.ToLower(val)
		}
		return val
	case int64, int32, int16, int8, int:
		return cast.ToInt(v)
	case float64, float32:
		return cast.ToFloat64(v)
	case time.Time:
		return val
	case []string:
		if stringToLower {
			return helpers.SliceToLower(val)
		}
		return v
	default:
		return v
	}
}

func getParamToLower(m resource.ResourceParamsProvider, key string) any {
	return getParam(m, key, true)
}

func (ps *pageState) initLazyProviders() error {
	ps.init.Add(func(ctx context.Context) (any, error) {
		pp, err := newPagePaths(ps)
		if err != nil {
			return nil, err
		}

		var outputFormatsForPage output.Formats
		var renderFormats output.Formats

		if ps.m.standaloneOutputFormat.IsZero() {
			outputFormatsForPage = ps.m.outputFormats()
			renderFormats = ps.s.h.renderFormats
		} else {
			// One of the fixed output format pages, e.g. 404.
			outputFormatsForPage = output.Formats{ps.m.standaloneOutputFormat}
			renderFormats = outputFormatsForPage
		}

		// Prepare output formats for all sites.
		// We do this even if this page does not get rendered on
		// its own. It may be referenced via one of the site collections etc.
		// it will then need an output format.
		ps.pageOutputs = make([]*pageOutput, len(renderFormats))
		created := make(map[string]*pageOutput)
		shouldRenderPage := !ps.m.noRender()

		for i, f := range renderFormats {

			if po, found := created[f.Name]; found {
				ps.pageOutputs[i] = po
				continue
			}

			render := shouldRenderPage
			if render {
				_, render = outputFormatsForPage.GetByName(f.Name)
			}

			po := newPageOutput(ps, pp, f, render)

			// Create a content provider for the first,
			// we may be able to reuse it.
			if i == 0 {
				contentProvider, err := newPageContentOutput(po)
				if err != nil {
					return nil, err
				}
				po.setContentProvider(contentProvider)
			}

			ps.pageOutputs[i] = po
			created[f.Name] = po

		}

		if err := ps.initCommonProviders(pp); err != nil {
			return nil, err
		}

		return nil, nil
	})

	return nil
}
