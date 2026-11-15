# Sources for prose.jsonl

`prose.jsonl` is a labelled set for measuring the grounding check on prose. Each line is one passage
(`context`) and the answers about it, each labelled `grounded` or not.

- The contexts are excerpts copied word for word from the sources below, fetched on 2026-11-15.
  Nothing in them was changed except cutting the article to its first paragraphs.
- The grounded answers are summaries written for this set. Every name, date, figure and quoted
  phrase in them is in the excerpt.
- The ungrounded answers are synthetic: a grounded summary with one detail swapped for a plausible
  wrong one, a name, a date, a quoted phrase or a number. `swap` says which, and the replacement
  appears nowhere in the excerpt.

## Licences

- **News, Wikinews**: text created after 16 December 2024 is under the
  [Creative Commons Attribution 4.0 licence](https://creativecommons.org/licenses/by/4.0/). Every
  article here was written in 2025 or 2026. Attribution: Wikinews contributors, at the revision
  linked. The excerpts were shortened; no other change was made.
- **Meeting minutes, Federal Reserve Board**: the Board's website says "Unless otherwise indicated,
  information on Board's website is in the public domain and may be copied and distributed without
  permission" and asks to be cited as the source
  ([disclaimer](https://www.federalreserve.gov/disclaimer.htm)). Source: Board of Governors of the
  Federal Reserve System.

## Passages

| id | kind | source | link | licence |
|---|---|---|---|---|
| `wn-closes` | news | Wikinews, "Wikimedia Foundation closes Wikinews after 21 years" (revision 5025600) | https://en.wikinews.org/w/index.php?oldid=5025600 | CC BY 4.0 |
| `wn-pope` | news | Wikinews, "Pope Leo XIV visits four nations in Africa" (revision 5024678) | https://en.wikinews.org/w/index.php?oldid=5024678 | CC BY 4.0 |
| `wn-musique` | news | Wikinews, "Musique Libre Femmes plays for Women's History Month at Tompkins Square Library in New York City" (revision 5024622) | https://en.wikinews.org/w/index.php?oldid=5024622 | CC BY 4.0 |
| `wn-oscars` | news | Wikinews, ""One Battle After Another" wins 6 Oscars including Best Picture" (revision 5024637) | https://en.wikinews.org/w/index.php?oldid=5024637 | CC BY 4.0 |
| `wn-caloundra` | news | Wikinews, "Caloundra win round 3 game in Australian soccer's Football Queensland Premier League 2 competition" (revision 5024638) | https://en.wikinews.org/w/index.php?oldid=5024638 | CC BY 4.0 |
| `wn-fuel` | news | Wikinews, "Australian Minister for Energy lowers fuel standards for 60 days amid rising oil prices, panic buying" (revision 5024646) | https://en.wikinews.org/w/index.php?oldid=5024646 | CC BY 4.0 |
| `wn-burwood` | news | Wikinews, "NSW government proposes rezoning to create residences midway between Sydney and Parramatta" (revision 5024648) | https://en.wikinews.org/w/index.php?oldid=5024648 | CC BY 4.0 |
| `wn-teheran` | news | Wikinews, "Former Major League Baseball pitcher Julio Teherán retires" (revision 5013866) | https://en.wikinews.org/w/index.php?oldid=5013866 | CC BY 4.0 |
| `wn-refuge` | news | Wikinews, "Australia grants refuge to five Iranian women's national soccer team players" (revision 5024650) | https://en.wikinews.org/w/index.php?oldid=5024650 | CC BY 4.0 |
| `wn-moreton` | news | Wikinews, "Moreton Bay illegally evicted homeless campers, Queensland Supreme Court rules" (revision 5024658) | https://en.wikinews.org/w/index.php?oldid=5024658 | CC BY 4.0 |
| `wn-monta` | news | Wikinews, "Monta publishes its 2025 EV charger ranking, including hardware performance" (revision 5024651) | https://en.wikinews.org/w/index.php?oldid=5024651 | CC BY 4.0 |
| `wn-scherzer` | news | Wikinews, "Max Scherzer signs deal to return to Toronto Blue Jays" (revision 5010345) | https://en.wikinews.org/w/index.php?oldid=5010345 | CC BY 4.0 |
| `wn-gorton` | news | Wikinews, "Green Party wins major by-election in northern England city of Manchester" (revision 5004410) | https://en.wikinews.org/w/index.php?oldid=5004410 | CC BY 4.0 |
| `wn-gobekli` | news | Wikinews, "Göbeklitepe, Taş Tepeler and life 12,000 years ago exhibition opens in Berlin, Germany" (revision 5024656) | https://en.wikinews.org/w/index.php?oldid=5024656 | CC BY 4.0 |
| `wn-weir` | news | Wikinews, "Grateful Dead rhythm guitarist Bob Weir dies, aged 78" (revision 5024659) | https://en.wikinews.org/w/index.php?oldid=5024659 | CC BY 4.0 |
| `wn-roblox` | news | Wikinews, "Roblox player uncovers cheating, awarded Guinness World Record" (revision 5024655) | https://en.wikinews.org/w/index.php?oldid=5024655 | CC BY 4.0 |
| `wn-parubiy` | news | Wikinews, "Kyiv street to be named for former Ukrainian parliament chairman Andriy Parubiy, killed in Lviv" (revision 5024635) | https://en.wikinews.org/w/index.php?oldid=5024635 | CC BY 4.0 |
| `wn-rahman` | news | Wikinews, "Opposition leader Tarique Rahman returns to Bangladesh after 17 years" (revision 5024628) | https://en.wikinews.org/w/index.php?oldid=5024628 | CC BY 4.0 |
| `wn-doctors` | news | Wikinews, "Resident doctors in England begin five-day strike during flu surge" (revision 4992425) | https://en.wikinews.org/w/index.php?oldid=4992425 | CC BY 4.0 |
| `wn-mi6` | news | Wikinews, "UK MI6 chief Blaise Metreweli warns of 'grey zone' warfare in first speech" (revision 4991136) | https://en.wikinews.org/w/index.php?oldid=4991136 | CC BY 4.0 |
| `wn-eurovision` | news | Wikinews, "Iceland pulls out of Eurovision 2026, joining four other countries to do so" (revision 4990173) | https://en.wikinews.org/w/index.php?oldid=4990173 | CC BY 4.0 |
| `wn-forest` | news | Wikinews, "Football: Nottingham Forest defeat FC Utrecht 2-1" (revision 4990179) | https://en.wikinews.org/w/index.php?oldid=4990179 | CC BY 4.0 |
| `wn-joyce` | news | Wikinews, "Australian MP Barnaby Joyce resigns from the National Party" (revision 4987161) | https://en.wikinews.org/w/index.php?oldid=4987161 | CC BY 4.0 |
| `wn-randwick` | news | Wikinews, "Randwick City Council opens public consultation about parking near beaches: ''To pay or not to pay?''" (revision 5010348) | https://en.wikinews.org/w/index.php?oldid=5010348 | CC BY 4.0 |
| `wn-cure` | news | Wikinews, "Keyboardist-guitarist of English rock band The Cure dies aged 65" (revision 5024631) | https://en.wikinews.org/w/index.php?oldid=5024631 | CC BY 4.0 |
| `fomc-jan-officers` | meeting minutes | Minutes of the Federal Open Market Committee, January 28-29, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250129.htm | public domain |
| `fomc-jan-vote` | meeting minutes | Minutes of the Federal Open Market Committee, January 28-29, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250129.htm | public domain |
| `fomc-jan-yields` | meeting minutes | Minutes of the Federal Open Market Committee, January 28-29, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250129.htm | public domain |
| `fomc-jan-prices` | meeting minutes | Minutes of the Federal Open Market Committee, January 28-29, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250129.htm | public domain |
| `fomc-jun-dates` | meeting minutes | Minutes of the Federal Open Market Committee, June 17-18, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250618.htm | public domain |
| `fomc-jun-tga` | meeting minutes | Minutes of the Federal Open Market Committee, June 17-18, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250618.htm | public domain |
| `fomc-jun-soma` | meeting minutes | Minutes of the Federal Open Market Committee, June 17-18, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250618.htm | public domain |
| `fomc-jun-srf` | meeting minutes | Minutes of the Federal Open Market Committee, June 17-18, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250618.htm | public domain |
| `fomc-jun-prices` | meeting minutes | Minutes of the Federal Open Market Committee, June 17-18, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250618.htm | public domain |
| `fomc-jun-trade` | meeting minutes | Minutes of the Federal Open Market Committee, June 17-18, 2025 | https://www.federalreserve.gov/monetarypolicy/fomcminutes20250618.htm | public domain |
