/* 0000: disable about:config warning ***/
user_pref("browser.aboutConfig.showWarning", false);
user_pref("browser.startup.page", 0);
user_pref("browser.startup.homepage", "chrome://browser/content/blanktab.html");
user_pref("browser.newtabpage.enabled", false);
user_pref("browser.newtabpage.activity-stream.showSponsored", false); // [FF58+] Sponsored stories
user_pref("browser.newtabpage.activity-stream.showSponsoredTopSites", false); // [FF83+] Sponsored shortcuts
user_pref("browser.newtabpage.activity-stream.showSponsoredCheckboxes", false); // [FF140+] Support Firefox
user_pref("browser.newtabpage.activity-stream.default.sites", "");

user_pref("geo.provider.ms-windows-location", false); // [WINDOWS]
user_pref("geo.provider.use_corelocation", false); // [MAC]
user_pref("geo.provider.use_geoclue", false); // [FF102+] [LINUX]
user_pref("extensions.getAddons.showPane", false); // [HIDDEN PREF]
user_pref("extensions.htmlaboutaddons.recommendations.enabled", false);
user_pref("browser.discovery.enabled", false);
user_pref("browser.newtabpage.activity-stream.feeds.telemetry", false);
user_pref("browser.newtabpage.activity-stream.telemetry", false);
user_pref("app.shield.optoutstudies.enabled", false);
user_pref("app.normandy.enabled", false);
user_pref("app.normandy.api_url", "");
user_pref("breakpad.reportURL", "");
user_pref("browser.tabs.crashReporting.sendReport", false); // [FF44+]
user_pref("browser.crashReports.unsubmittedCheck.autoSubmit2", false); // [DEFAULT: false]
user_pref("captivedetect.canonicalURL", "");
user_pref("network.captive-portal-service.enabled", false); // [FF52+]
user_pref("network.connectivity-service.enabled", false);
user_pref("browser.safebrowsing.downloads.remote.enabled", false);
user_pref("network.http.speculative-parallel-limit", 0);
user_pref("browser.places.speculativeConnect.enabled", false);
user_pref("network.proxy.socks_remote_dns", true);
user_pref("network.file.disable_unc_paths", true); // [HIDDEN PREF]
user_pref("network.gio.supported-protocols", ""); // [HIDDEN PREF] [DEFAULT: ""]
user_pref("browser.urlbar.speculativeConnect.enabled", false);
user_pref("browser.urlbar.quicksuggest.enabled", false); // [FF92+]
user_pref("browser.urlbar.suggest.quicksuggest.nonsponsored", false); // [FF95+]
user_pref("browser.urlbar.suggest.quicksuggest.sponsored", false); // [FF92+]
user_pref("browser.search.suggest.enabled", false);
user_pref("browser.urlbar.suggest.searches", false);
user_pref("browser.urlbar.trending.featureGate", false);
user_pref("browser.urlbar.addons.featureGate", false); // [FF115+]
user_pref("browser.urlbar.amp.featureGate", false); // [FF141+] adMarketplace
user_pref("browser.urlbar.fakespot.featureGate", false); // [FF130+] [DEFAULT: false]
user_pref("browser.urlbar.mdn.featureGate", false); // [FF117+]
user_pref("browser.urlbar.weather.featureGate", false); // [FF108+]
user_pref("browser.urlbar.wikipedia.featureGate", false); // [FF141+]
user_pref("browser.urlbar.yelp.featureGate", false); // [FF124+]
user_pref("browser.formfill.enable", false);
user_pref("browser.search.separatePrivateDefault", true); // [FF70+]
user_pref("browser.search.separatePrivateDefault.ui.enabled", true); // [FF71+]

user_pref("signon.autofillForms", false);
user_pref("signon.formlessCapture.enabled", false);
user_pref("network.auth.subresource-http-auth-allow", 1);
user_pref("browser.privatebrowsing.forceMediaMemoryCache", true); // [FF75+]
user_pref("media.memory_cache_max_size", 65536);
user_pref("browser.sessionstore.privacy_level", 2);
user_pref("toolkit.winRegisterApplicationRestart", false);
user_pref("browser.shell.shortcutFavicons", false);
user_pref("security.ssl.require_safe_negotiation", true);
user_pref("security.tls.enable_0rtt_data", false);
user_pref("security.OCSP.enabled", 1); // [DEFAULT: 1]
user_pref("security.OCSP.require", true);
user_pref("security.cert_pinning.enforcement_level", 2);
user_pref("security.remote_settings.crlite_filters.enabled", true); // [DEFAULT: true FF137+]
user_pref("security.pki.crlite_mode", 2);
user_pref("dom.security.https_only_mode_send_http_background_request", false);
user_pref("security.ssl.treat_unsafe_negotiation_as_broken", true);
user_pref("browser.xul.error_pages.expert_bad_cert", true);
user_pref("network.http.referer.XOriginTrimmingPolicy", 2);
user_pref("privacy.userContext.enabled", true);
user_pref("privacy.userContext.ui.enabled", true);
user_pref("media.peerconnection.ice.proxy_only_if_behind_proxy", true);
user_pref("media.peerconnection.ice.default_address_only", true);
user_pref("dom.disable_window_move_resize", true);
user_pref("browser.download.start_downloads_in_tmp_dir", true); // [FF102+]
user_pref("browser.helperApps.deleteTempFileOnExit", true);
user_pref("browser.uitour.enabled", false);
user_pref("devtools.debugger.remote-enabled", false); // [DEFAULT: false]
user_pref("permissions.manager.defaultsUrl", "");
user_pref("network.IDN_show_punycode", true);
user_pref("pdfjs.disabled", false); // [DEFAULT: false]
user_pref("pdfjs.enableScripting", false); // [FF86+]
user_pref("browser.tabs.searchclipboardfor.middleclick", false); // [DEFAULT: false NON-LINUX]
user_pref("browser.contentanalysis.enabled", false); // [FF121+] [DEFAULT: false]
user_pref("browser.contentanalysis.default_result", 0); // [FF127+] [DEFAULT: 0]
user_pref("security.csp.reporting.enabled", false);
user_pref("browser.download.alwaysOpenPanel", false);
user_pref("browser.download.always_ask_before_handling_new_types", true);
user_pref("browser.contentblocking.category", "strict"); // [HIDDEN PREF]
user_pref("privacy.resistFingerprinting.block_mozAddonManager", true);
user_pref("privacy.spoof_english", 1);
user_pref("browser.link.open_newwindow.restriction", 0);
user_pref("extensions.blocklist.enabled", true); // [DEFAULT: true]
user_pref("network.http.referer.spoofSource", false); // [DEFAULT: false]
user_pref("security.dialog_enable_delay", 1000); // [DEFAULT: 1000]
user_pref("privacy.firstparty.isolate", false); // [DEFAULT: false]
user_pref("extensions.webcompat.enable_shims", true); // [HIDDEN PREF] [DEFAULT: true]
user_pref("security.tls.version.enable-deprecated", false); // [DEFAULT: false]
user_pref("extensions.webcompat-reporter.enabled", false); // [DEFAULT: false]
user_pref("extensions.quarantinedDomains.enabled", true); // [DEFAULT: true]
user_pref("datareporting.policy.dataSubmissionEnabled", false);
user_pref("datareporting.healthreport.uploadEnabled", false);
user_pref("toolkit.telemetry.unified", false);
user_pref("toolkit.telemetry.enabled", false); // see [NOTE]
user_pref("toolkit.telemetry.server", "data:,");
user_pref("toolkit.telemetry.archive.enabled", false);
user_pref("toolkit.telemetry.newProfilePing.enabled", false); // [FF55+]
user_pref("toolkit.telemetry.shutdownPingSender.enabled", false); // [FF55+]
user_pref("toolkit.telemetry.updatePing.enabled", false); // [FF56+]
user_pref("toolkit.telemetry.bhrPing.enabled", false); // [FF57+] Background Hang Reporter
user_pref("toolkit.telemetry.firstShutdownPing.enabled", false); // [FF57+]
user_pref("toolkit.telemetry.coverage.opt-out", true); // [HIDDEN PREF]
user_pref("toolkit.coverage.opt-out", true); // [FF64+] [HIDDEN PREF]
user_pref("toolkit.coverage.endpoint.base", "");
user_pref("browser.startup.homepage_override.mstone", "ignore"); // [HIDDEN PREF]
user_pref("browser.newtabpage.activity-stream.asrouter.userprefs.cfr.addons", false);
user_pref("browser.newtabpage.activity-stream.asrouter.userprefs.cfr.features", false);
user_pref("browser.urlbar.showSearchTerms.enabled", false);

// BEGIN PERSONAL PREFS
user_pref("sidebar.verticalTabs", true);

user_pref("sidebar.visibility", "expand-on-hover");
user_pref("sidebar.verticalTabs.dragToPinPromo.dismissed", true);
user_pref("sidebar.main.tools", "syncedtabs,history,bookmarks");
user_pref("browser.toolbars.bookmarks.visibility", "never");
user_pref("font.name.serif.x-western", "Bitstream Vera Sans");