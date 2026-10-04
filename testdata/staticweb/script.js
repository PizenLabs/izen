/* ==========================================================================
   Tom Hunter — AI Engineer
   Progressive enhancement only: the page is fully readable without this file.
   ========================================================================== */
(function () {
  "use strict";

  var doc = document;
  var root = doc.documentElement;

  function revealAll() {
    var els = doc.querySelectorAll(".reveal");
    for (var i = 0; i < els.length; i++) {
      els[i].classList.add("is-visible");
    }
  }

  try {
    /* ── Theme ──────────────────────────────────────────────────────────
       Precedence: explicit visitor choice (localStorage) > system setting.
       We never force a theme until the visitor actually chooses one, so a
       change to the OS theme is reflected live. */
    var THEME_KEY = "th-theme";
    var toggle = doc.querySelector(".theme-toggle");

    function systemPrefersDark() {
      return Boolean(
        window.matchMedia &&
          window.matchMedia("(prefers-color-scheme: dark)").matches
      );
    }

    function effectiveTheme() {
      return root.getAttribute("data-theme") || (systemPrefersDark() ? "dark" : "light");
    }

    function refreshToggle(theme) {
      if (!toggle) return;
      var isDark = theme === "dark";
      toggle.setAttribute("aria-pressed", String(isDark));
      toggle.setAttribute(
        "aria-label",
        isDark ? "Switch to light theme" : "Switch to dark theme"
      );
    }

    function applyTheme(theme, persist) {
      root.setAttribute("data-theme", theme);
      refreshToggle(theme);
      if (persist) {
        try {
          localStorage.setItem(THEME_KEY, theme);
        } catch (e) {
          /* storage unavailable — theme still applies for this visit */
        }
      }
    }

    var stored = null;
    try {
      stored = localStorage.getItem(THEME_KEY);
    } catch (e) {
      stored = null;
    }

    if (stored === "dark" || stored === "light") {
      root.setAttribute("data-theme", stored);
    }
    refreshToggle(effectiveTheme());

    if (toggle) {
      toggle.addEventListener("click", function () {
        applyTheme(effectiveTheme() === "dark" ? "light" : "dark", true);
      });
    }

    if (window.matchMedia) {
      var mq = window.matchMedia("(prefers-color-scheme: dark)");
      var onSystemChange = function () {
        if (!root.getAttribute("data-theme")) {
          refreshToggle(effectiveTheme());
        }
      };
      if (mq.addEventListener) {
        mq.addEventListener("change", onSystemChange);
      } else if (mq.addListener) {
        mq.addListener(onSystemChange);
      }
    }

    /* ── Mobile navigation ──────────────────────────────────────────── */
    var navToggle = doc.querySelector(".nav-toggle");
    var nav = doc.getElementById("primary-nav");

    function setNav(open) {
      if (!nav || !navToggle) return;
      nav.classList.toggle("is-open", open);
      navToggle.setAttribute("aria-expanded", String(open));
      navToggle.setAttribute(
        "aria-label",
        open ? "Close navigation menu" : "Open navigation menu"
      );
    }

    if (navToggle && nav) {
      navToggle.addEventListener("click", function () {
        setNav(!nav.classList.contains("is-open"));
      });

      nav.addEventListener("click", function (event) {
        if (event.target && event.target.closest && event.target.closest("a")) {
          setNav(false);
        }
      });

      doc.addEventListener("keydown", function (event) {
        if (event.key === "Escape" && nav.classList.contains("is-open")) {
          setNav(false);
          navToggle.focus();
        }
      });

      window.addEventListener("resize", function () {
        if (window.innerWidth > 720) setNav(false);
      });
    }

    /* ── Header elevation on scroll ─────────────────────────────────── */
    var header = doc.querySelector(".site-header");
    function onScroll() {
      if (!header) return;
      header.classList.toggle("is-scrolled", window.scrollY > 8);
    }
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });

    /* ── Active section in navigation ───────────────────────────────── */
    var navLinks = Array.prototype.slice.call(
      doc.querySelectorAll('.primary-nav a[href^="#"]')
    );
    var sections = [];
    for (var n = 0; n < navLinks.length; n++) {
      var id = navLinks[n].getAttribute("href");
      var section = id && id.length > 1 ? doc.querySelector(id) : null;
      if (section) sections.push({ link: navLinks[n], section: section });
    }

    if ("IntersectionObserver" in window && sections.length) {
      var navObserver = new IntersectionObserver(
        function (entries) {
          for (var e = 0; e < entries.length; e++) {
            if (!entries[e].isIntersecting) continue;
            var active = entries[e].target;
            for (var s = 0; s < sections.length; s++) {
              if (sections[s].section === active) {
                sections[s].link.setAttribute("aria-current", "true");
              } else {
                sections[s].link.removeAttribute("aria-current");
              }
            }
          }
        },
        { rootMargin: "-45% 0px -50% 0px", threshold: 0 }
      );
      for (var k = 0; k < sections.length; k++) {
        navObserver.observe(sections[k].section);
      }
    }

    /* ── Reveal-on-scroll ───────────────────────────────────────────── */
    var reduceMotion =
      window.matchMedia &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    if (!("IntersectionObserver" in window) || reduceMotion) {
      revealAll();
    } else {
      var revealObserver = new IntersectionObserver(
        function (entries, observer) {
          for (var r = 0; r < entries.length; r++) {
            if (entries[r].isIntersecting) {
              entries[r].target.classList.add("is-visible");
              observer.unobserve(entries[r].target);
            }
          }
        },
        { rootMargin: "0px 0px -10% 0px", threshold: 0.05 }
      );
      var targets = doc.querySelectorAll(".reveal");
      for (var t = 0; t < targets.length; t++) {
        revealObserver.observe(targets[t]);
      }
      /* Safety net: if observation never fires, nothing stays hidden. */
      window.setTimeout(revealAll, 2500);
    }

    /* ── Footer year ────────────────────────────────────────────────── */
    var year = doc.getElementById("year");
    if (year) year.textContent = String(new Date().getFullYear());
  } catch (err) {
    /* Never let an enhancement failure hide the portfolio. */
    revealAll();
  }
})();
