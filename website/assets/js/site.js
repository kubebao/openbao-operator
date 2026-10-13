(() => {
  const body = document.body;

  const navToggle = document.querySelector('[data-nav-toggle]');
  const navClose = document.querySelector('[data-nav-close]');
  const sidebar = document.querySelector('[data-sidebar]');
  const primaryNavigation = document.querySelector('#primary-navigation');
  const navTarget = sidebar || primaryNavigation;
  const mobileNav = window.matchMedia('(max-width: 48rem)');
  const sidebarCollapse = document.querySelector('[data-sidebar-collapse]');
  const sidebarCollapseLabel = document.querySelector('[data-sidebar-collapse-label]');
  const sidebarStorageKey = 'openbao-docs-sidebar-collapsed';

  function focusableElements(container) {
    return [...container.querySelectorAll('a[href], summary, button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])')]
      .filter((element) => !element.hidden && element.getClientRects().length > 0
        && !element.closest('details:not([open]) > :not(summary)'));
  }

  function syncNavAccessibility(open) {
    if (!navTarget) return;
    if (mobileNav.matches) {
      navTarget.inert = !open;
      navTarget.setAttribute('aria-hidden', String(!open));
    } else {
      navTarget.inert = false;
      navTarget.removeAttribute('aria-hidden');
    }
  }

  function setNav(open, restoreFocus = true) {
    if (!navTarget || !navToggle) return;
    const wasOpen = body.classList.contains('nav-open');
    const next = mobileNav.matches && open;
    body.classList.toggle('nav-open', next);
    navToggle.setAttribute('aria-expanded', String(next));
    navToggle.setAttribute('aria-label', `${next ? 'Close' : 'Open'} ${sidebar ? 'documentation' : 'primary'} navigation`);
    syncNavAccessibility(next);
    if (next) {
      window.setTimeout(() => focusableElements(navTarget)[0]?.focus(), 20);
    } else if (wasOpen && restoreFocus) {
      navToggle.focus();
    }
  }

  navToggle?.addEventListener('click', () => setNav(!body.classList.contains('nav-open')));
  navClose?.addEventListener('click', () => setNav(false));
  navTarget?.querySelectorAll('a').forEach((link) => link.addEventListener('click', () => setNav(false, false)));
  mobileNav.addEventListener('change', () => setNav(false, false));
  syncNavAccessibility(false);

  function setSidebarCollapsed(collapsed) {
    body.classList.toggle('sidebar-collapsed', collapsed);
    sidebarCollapse?.setAttribute('aria-expanded', String(!collapsed));
    if (sidebarCollapse) sidebarCollapse.title = collapsed ? 'Expand sidebar' : 'Collapse sidebar';
    if (sidebarCollapseLabel) sidebarCollapseLabel.textContent = collapsed ? 'Expand sidebar' : 'Collapse sidebar';
  }

  if (sidebarCollapse) {
    let collapsed = false;
    try {
      collapsed = window.localStorage.getItem(sidebarStorageKey) === 'true';
    } catch (_) {
      // Storage can be unavailable in hardened browser contexts.
    }
    setSidebarCollapsed(collapsed);
    sidebarCollapse.addEventListener('click', () => {
      const next = !body.classList.contains('sidebar-collapsed');
      setSidebarCollapsed(next);
      try {
        window.localStorage.setItem(sidebarStorageKey, String(next));
      } catch (_) {
        // The control still works for the current page when storage is unavailable.
      }
    });
  }

  const stargazersLink = document.querySelector('[data-stargazers]');
  const stargazerCount = stargazersLink?.querySelector('[data-stargazer-count]');
  const stargazerCacheKey = 'openbao-docs-stargazers';
  const stargazerCacheTTL = 6 * 60 * 60 * 1000;

  function showStargazerCount(count) {
    if (!stargazersLink || !stargazerCount || !Number.isSafeInteger(count) || count < 0) return;
    stargazerCount.textContent = new Intl.NumberFormat().format(count);
    stargazerCount.hidden = false;
    stargazersLink.setAttribute('aria-label', `OpenBao Operator on GitHub, ${count} stars`);
  }

  if (stargazersLink?.dataset.endpoint) {
    let cached;
    try {
      cached = JSON.parse(window.localStorage.getItem(stargazerCacheKey));
    } catch (_) {
      // Fetch the current count when storage is unavailable or invalid.
    }

    if (cached && Date.now() - cached.updatedAt < stargazerCacheTTL) {
      showStargazerCount(cached.count);
    } else {
      fetch(stargazersLink.dataset.endpoint, {
        headers: { Accept: 'application/vnd.github+json' }
      })
        .then((response) => {
          if (!response.ok) throw new Error(`GitHub repository metadata returned ${response.status}`);
          return response.json();
        })
        .then((repository) => {
          showStargazerCount(repository.stargazers_count);
          try {
            window.localStorage.setItem(stargazerCacheKey, JSON.stringify({
              count: repository.stargazers_count,
              updatedAt: Date.now()
            }));
          } catch (_) {
            // The live count remains visible when storage is unavailable.
          }
        })
        .catch(() => {
          // Keep the GitHub repository link usable when the API is unavailable.
        });
    }
  }

  document.querySelectorAll('[data-copy-button]').forEach((button) => {
    button.addEventListener('click', async () => {
      const code = button.closest('.command-block')?.querySelector('code')?.textContent;
      if (!code) return;
      try {
        await navigator.clipboard.writeText(code);
        button.textContent = 'Copied';
      } catch (_) {
        button.textContent = 'Copy failed';
      }
      window.setTimeout(() => { button.textContent = 'Copy'; }, 1400);
    });
  });

  const dialog = document.querySelector('[data-search-dialog]');
  const input = dialog?.querySelector('[data-search-input]');
  const results = dialog?.querySelector('[data-search-results]');
  const state = dialog?.querySelector('[data-search-state]');
  const openButtons = document.querySelectorAll('[data-search-open]');
  const closeButtons = dialog?.querySelectorAll('[data-search-close]') || [];
  let indexPromise;
  let resultLinks = [];
  let selectedIndex = -1;
  let searchReturnTarget;

  function loadIndex() {
    if (!indexPromise) {
      indexPromise = fetch(dialog.dataset.searchIndex)
        .then((response) => {
          if (!response.ok) throw new Error(`Search index returned ${response.status}`);
          return response.json();
        })
        .catch((error) => {
          indexPromise = undefined;
          throw error;
        });
    }
    return indexPromise;
  }

  function setSearch(open) {
    if (!dialog || !input) return;
    if (open) {
      if (dialog.open) return;
      searchReturnTarget = document.activeElement;
      setNav(false, false);
      dialog.showModal();
      body.classList.add('search-open');
      loadIndex().catch(() => {
        state.innerHTML = '<div><p>Search is unavailable</p><span>Rebuild the site to regenerate the index.</span></div>';
      });
      window.setTimeout(() => input.focus(), 20);
    } else if (dialog.open) {
      dialog.close();
    }
  }

  function resetSearch() {
    if (!input || !state) return;
    body.classList.remove('search-open');
    input.value = '';
    state.innerHTML = '<div><p id="search-title">Search the handbook</p><span>Try “install”, “threat model”, or “compatibility”.</span></div>';
    renderResults([]);
    searchReturnTarget?.focus();
    searchReturnTarget = undefined;
  }

  function scorePage(page, terms) {
    const title = page.title.toLowerCase();
    const summary = page.summary.toLowerCase();
    const content = page.content.toLowerCase();
    let score = 0;
    for (const term of terms) {
      if (!content.includes(term) && !summary.includes(term) && !title.includes(term)) return -1;
      if (title === term) score += 80;
      else if (title.startsWith(term)) score += 40;
      else if (title.includes(term)) score += 24;
      if (summary.includes(term)) score += 8;
      if (content.includes(term)) score += 2;
    }
    return score;
  }

  function renderResults(pages) {
    if (!results || !state) return;
    results.replaceChildren();
    selectedIndex = -1;
    resultLinks = [];
    state.hidden = pages.length > 0;

    for (const page of pages) {
      const item = document.createElement('li');
      const link = document.createElement('a');
      const meta = document.createElement('span');
      const title = document.createElement('strong');
      const summary = document.createElement('p');
      link.href = page.url;
      meta.textContent = page.section;
      title.textContent = page.title;
      summary.textContent = page.summary;
      link.append(meta, title, summary);
      item.append(link);
      results.append(item);
      resultLinks.push(link);
    }
  }

  function selectResult(next) {
    if (!resultLinks.length) return;
    selectedIndex = (next + resultLinks.length) % resultLinks.length;
    resultLinks.forEach((link, index) => link.classList.toggle('is-selected', index === selectedIndex));
    resultLinks[selectedIndex].focus();
    resultLinks[selectedIndex].scrollIntoView({ block: 'nearest' });
  }

  openButtons.forEach((button) => button.addEventListener('click', () => setSearch(true)));
  closeButtons.forEach((button) => button.addEventListener('click', () => setSearch(false)));
  dialog?.addEventListener('close', resetSearch);
  dialog?.addEventListener('cancel', (event) => {
    event.preventDefault();
    setSearch(false);
  });
  dialog?.addEventListener('click', (event) => {
    if (event.target === dialog) setSearch(false);
  });

  input?.addEventListener('input', async () => {
    const query = input.value.trim().toLowerCase();
    if (query.length < 2) {
      state.innerHTML = '<div><p id="search-title">Search the handbook</p><span>Type at least two characters.</span></div>';
      renderResults([]);
      return;
    }
    const terms = query.split(/\s+/).filter(Boolean);
    try {
      const pages = await loadIndex();
      const matches = pages
        .map((page) => ({ page, score: scorePage(page, terms) }))
        .filter((item) => item.score >= 0)
        .sort((a, b) => b.score - a.score || a.page.title.localeCompare(b.page.title))
        .slice(0, 8)
        .map((item) => item.page);

      if (!matches.length) {
        state.innerHTML = '<div><p>No matching pages</p><span>Try a broader operator term.</span></div>';
      }
      renderResults(matches);
    } catch (_) {
      renderResults([]);
      state.innerHTML = '<div><p>Search is unavailable</p><span>Try again after reloading the page.</span></div>';
    }
  });

  document.addEventListener('keydown', (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      setSearch(true);
      return;
    }
    if (dialog?.open) {
      if (event.key === 'Escape') { event.preventDefault(); setSearch(false); }
      if (event.key === 'ArrowDown') { event.preventDefault(); selectResult(selectedIndex + 1); }
      if (event.key === 'ArrowUp') { event.preventDefault(); selectResult(selectedIndex - 1); }
      return;
    }
    if (!body.classList.contains('nav-open')) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      setNav(false);
      return;
    }
    if (event.key === 'Tab' && navTarget) {
      const focusable = focusableElements(navTarget);
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
  });
})();
