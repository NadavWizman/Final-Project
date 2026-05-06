# מדריך לימוד — מערכת מסחר מבוזרת
## איך לעבור על הפרויקט שלב אחרי שלב כדי להגיש אותו

המסמך הזה הוא הליווי שלך ללימוד הקוד. הוא בנוי כסדר קריאה מלמטה למעלה — בדיוק
איך שהיית בונה את המערכת בעצמך מאפס. כל שכבה מתבססת על הקודמת לה. אם תקראי לפי
הסדר הזה, כל קובץ יהיה הגיוני בזכות מה שלמדת קודם.

---

# חלק 1 — הרעיון הכללי בעמוד אחד

המערכת מאפשרת למשתמשים לקנות ולמכור מניות מ־S&P 500 דרך REST API, אבל
ההחלטה אם פקודה מתבצעת או נדחית **לא** מתקבלת על ידי שרת מרכזי אחד. במקום
זאת, יש שלושה Nodes עצמאיים שכתובים ב־Go, וכל פקודה מתבצעת רק אם לפחות
2 מתוך 3 מהם מסכימים שהיא חוקית. הסכמה כזו נקראת **קונצנזוס**, והמנגנון
הספציפי שאנחנו משתמשים בו נקרא **Proof-of-Authority (PoA)**.

מאחר שכל מסחר נשמר בשרשרת בלוקים בלתי ניתנת לשינוי (blockchain), אי אפשר
"למחוק" עסקה בדיעבד. המחירים מגיעים משירות חיצוני שנקרא **Oracle** ששולף
מחירים מ־Yahoo Finance ומפיץ אותם לכל ה־Nodes עם חותמות זמן. כל פקודה
חתומה דיגיטלית בחתימת המשתמש (ECDSA) — כך שאי אפשר לזייף פקודות.

**שלוש מילות מפתח שתצטרכי לזכור לאורך כל הפרויקט:**

1. **Blockchain** — שרשרת של בלוקים. כל בלוק מכיל hash של הקודם, ולכן אי אפשר
   לשנות בלוק היסטורי בלי לפסול את כל מה שבא אחריו.
2. **PoA Consensus** — קבוצה קבועה של "רשויות" (אצלנו 3 Nodes) חותמות על בלוקים.
   רוב מקרב הרשויות (2 מתוך 3) מספיק לאישור.
3. **ECDSA** — חתימה דיגיטלית מבוססת עקומה אליפטית. המשתמש מחזיק במפתח פרטי,
   ה־Nodes מחזיקים במפתח הציבורי שלו, ואין דרך לזייף חתימה בלי המפתח הפרטי.

---

# חלק 2 — מושגים שצריך ללמוד לפני שנוגעים בקוד

לפני שתתחילי לקרוא קבצים, אם אחד מהמושגים הבאים לא מוכר — קחי שעה לחפש
אותו ב־YouTube/Wikipedia. זה ישתלם.

**רמת חובה (חייבת להבין):**
- **Hash function** (SHA-256 ספציפית) — מה היא עושה ולמה היא דטרמיניסטית.
- **Public/private keypair** — איך זה עובד ב־ECDSA.
- **Digital signature** — מה ההבדל בין חתימה לבין הצפנה.
- **Nonce** ו־Replay Attack — למה צריך מונה כדי למנוע שידור חוזר של פקודה.
- **gRPC** ו־Protobuf — סדרת שדות, schema, RPC vs REST.
- **Django REST Framework** — Models, Views, Serializers.

**רמת הבנה (לא חייבת לדעת ליישם, אבל חייבת להסביר במילים):**
- **Merkle tree** — איך מרכיבים hash אחד מתוך רשימת עסקאות.
- **PoA vs PoW vs PoS** — למה אצלנו זה PoA ולא Proof-of-Work כמו Bitcoin.
- **Quorum** ו־Byzantine fault tolerance — מה המשמעות של "2 מתוך 3".
- **Oracle problem** — למה זו בעיה קשה לחבר נתונים מהעולם האמיתי לבלוקצ'יין.

**רמה כללית (טוב לדעת):**
- **gunicorn / WSGI** — איך Django רץ בפרודקשן.
- **Docker compose** — איך 5 שירותים מתחברים זה לזה.

---

# חלק 3 — היררכית הקבצים מלמטה למעלה

המספור פה הוא **סדר הלימוד**. תקראי קובץ אחר קובץ ביחד עם ההסבר. אם תעקבי
לפי הסדר הזה, בכל שלב יהיו לך כל הכלים להבין את השלב הבא.

---

## שכבה 0 — חוזה ה־API (Single Source of Truth)

**קובץ אחד בלבד, אבל הכי חשוב:**

### `proto/trading.proto`

זה הקובץ הראשון שכתבתי. למה? כי הוא מגדיר את **השפה** שבה כל החלקים של
המערכת מדברים זה עם זה. Django, ה־3 Nodes, וה־Oracle — כולם קומפלים מהקובץ
הזה את הקוד שלהם.

**מה לחפש כשאת קוראת:**
- **3 שירותים** (`service`): `TradingService`, `ConsensusService`, `OracleSink`.
- **שני סוגי הודעות מרכזיות**: `OrderTx` (פקודה לפני חתימה) ו־`SignedOrderTx`
  (פקודה חתומה — זה מה שעובר על הרשת).
- **שדה `nonce`** — תזכרי מאיפה הוא בא, נראה אותו שוב בהמשך.
- **שדה `price_timestamp`** — לבדיקת Stale-Price.
- **שדה `limit_price`** — הוספנו אותו אחרי שראינו שזה היה חסר במסמך הדרישות.

**למה הקובץ הזה ראשון בלימוד:** כי הוא מגדיר את **גבולות** המערכת. כל פעם
שלא יהיה לך ברור איזה מידע עובר בין שירותים, את חוזרת לקובץ הזה.

---

## שכבה 1 — קריפטוגרפיה (החתימות הדיגיטליות)

**שני קבצים — אחד בכל צד:**

### `node/internal/crypto/ecdsa.go` (Go — צד הוולידטור)
### `django_app/orders/signer.py` (Python — צד החותם)

**הרעיון:** למשתמשת יש זוג מפתחות ECDSA P-256. Django חותם בשמה על כל
פקודה (`OrderTx`) באמצעות המפתח הפרטי. ה־Nodes מאמתים את החתימה באמצעות
המפתח הציבורי המוטמע בתוך ה־`SignedOrderTx`.

**הקריטיות בשני הקבצים האלה:** הם **חייבים להסכים בייט-בייט** על:
1. איך מקודדים את ה־`OrderTx` לבייטים (Protobuf serialization).
2. איזו פונקציית hash משתמשים (SHA-256).
3. איך מקודדים את החתימה (ASN.1 DER).
4. איך מקודדים את המפתח הציבורי (DER SubjectPublicKeyInfo).

אם משהו אחד יהיה שונה בין Python ל־Go — אף חתימה לעולם לא תאומת. שווה לקרוא
את ההערות בראש כל קובץ — כתבתי שם בדיוק איזה פורמט נבחר ולמה.

**מה לבדוק שאת מבינה:**
- מה ההבדל בין `LoadPrivateKeyPEM` ל־`MarshalPublicKeyDER`?
- למה אנחנו חותמים על `sha256(message)` ולא על המסר עצמו?
- מי שולח את המפתח הציבורי — המשתמשת בכל פקודה, או שמור בצד ה־Node מראש?
  (תשובה: שניהם — נשלח בכל פקודה ומאומת מול הרשום ב־state).

---

## שכבה 2 — מצב המערכת (State Machine)

### `node/internal/state/state.go`

זה הלב של ה־Node מבחינה עסקית. הקובץ הזה לא יודע כלום על בלוקצ'יין, על
קונצנזוס, או על gRPC — הוא רק יודע:
- מי המשתמשים, כמה כסף ומניות יש לכל אחד, מה ה־nonce הנוכחי שלו.
- איזה סמלים מותרים (S&P 500 whitelist).
- **איך לבדוק** האם פקודה תקפה (`Validate`) — בלי לשנות את ה־state.
- **איך להחיל** פקודה אם היא אושרה (`Apply`) — מעדכן יתרות ו־nonce.

**ההפרדה בין `Validate` ל־`Apply` קריטית:** הוולידטורים קוראים ל־`Validate`
לפני שהם מצביעים. ה־`Apply` רץ רק אחרי שהבלוק התקבל בקונצנזוס. ככה שאם בלוק
נדחה — שום מצב לא שונה.

**הבדיקות בתוך `Validate`** (שווה לקרוא אותן כי זו תמצית האבטחה של המערכת):
1. הסמל ב־whitelist?
2. ה־user קיים?
3. ה־nonce חדש (גדול מהאחרון שיושם)?
4. כמות חיובית?
5. מחיר חיובי?
6. **limit_price מתקיים?** (זה השדה החדש שהוספנו)
7. יש מספיק כסף (BUY) או מספיק מניות (SELL)?

**שדה אחד שכדאי לזכור:** `Account.LastNonce`. זה הליבה של ההגנה מ־Replay
Attacks. כל בקשה חייבת nonce גדול ממה ששמור פה, אחרת היא נדחית.

---

## שכבה 3 — בלוקים ושרשרת

**שני קבצים, באותה תיקיה:**

### `node/internal/blockchain/block.go`
### `node/internal/blockchain/chain.go`

`block.go` הוא קצר אבל חשוב: הוא מגדיר איך מחשבים את ה־hash של בלוק
(`Hash` function), איך בונים merkle tree מרשימת עסקאות (`MerkleRoot`),
ואיך מאמתים שבלוק "ב" באמת המשך של בלוק "א" (`ValidateLink`).

הנקודה הקריטית פה: **חישוב ה־hash חייב להיות דטרמיניסטי**. אם node-1
מחשב hash אחד ו־node-2 מחשב hash אחר — קונצנזוס בלתי אפשרי. לכן הסדר של
השדות בתוך `Hash()` הוא נוקשה: index, prev_hash, timestamp, merkle root.

`chain.go` הוא ה־storage layer. הוא שומר את הבלוקים בקובץ JSON-Lines
(שורה אחת לכל בלוק, base64 של הפרוטובאף), ועם פעולה אחת בלבד —
`Append` — שמוסיפה בלוק לאחר ש־`ValidateLink` ו־`VerifyHash` עברו.

**למה שמירה כזו פשוטה מספיקה:** כי ה־state יכול להיבנות מחדש על ידי הרצה
מחדש של כל הבלוקים מבראשית. אם node קורס — בעלייה הבאה הוא קורא את הקובץ,
מריץ `Apply` על כל בלוק, ומגיע לאותו מצב כמו אחיו.

---

## שכבה 4 — Oracle (זרם המחירים)

**שני צדדים:**

### `node/internal/oracleclient/cache.go` (צד ה־Node — מקבל מחירים)
### `oracle/oracle.py` (השירות עצמו — שולח מחירים)

`cache.go` הוא ה־in-memory cache שכל Node שומר בו את המחיר העדכני ביותר
לכל סמל. שתי הפונקציות החשובות:
- `Put(quote)` — מעדכן רק אם ה־timestamp חדש יותר. הגנה מפני שליחה חוזרת
  של מחיר ישן.
- `CheckFreshness(symbol, txTimestamp)` — בודק שלא עברו יותר מ־30 שניות
  מאז שה־tx נחתם. זו ה־Stale-Price Check.

`oracle.py` הוא השירות שרץ ברקע. כל 15 שניות:
1. שואב מחירים מ־Yahoo Finance בעזרת ספריית `yfinance`.
2. דוחף את כל המחירים לשלושת ה־Nodes דרך gRPC.
3. גם מציע REST endpoint (`/price/<symbol>`) ש־Django משתמש בו לפני
   שמירת פקודה.

---

## שכבה 5 — מנוע הקונצנזוס (הלב של המערכת)

### `node/internal/consensus/poa.go`

זה הקובץ המורכב ביותר בפרויקט, אבל אם הגעת עד פה לפי הסדר — כל מה שהוא
משתמש בו (state, blockchain, oracle, crypto) כבר מוכר לך.

**שתי פונקציות מרכזיות:**

**1. `RunLeaderRound` — מה שה־Leader עושה כשהוא מקבל פקודה חדשה:**
- מאמת את הפקודה אצלו (סינון מוקדם — חוסך סבב קונצנזוס מיותר).
- בונה בלוק מועמד שמרחיב את הראש הנוכחי של השרשרת.
- שולח `ProposeBlock` לכל ה־Validators במקביל (גורוטינות).
- סופר אישורים. אם הגיעו לקוורום (2 מתוך 3) — חותם, מוסיף לשרשרת,
  ומשדר `CommitBlock` לכולם.
- אם לא הגיע לקוורום — מחזיר שגיאה לפונה (Django).

**2. `HandleProposal` — מה שכל Validator עושה כשהוא מקבל הצעת בלוק:**
- בודק שהבלוק קושר נכון לראש שלו (אותו prev_hash).
- מריץ את אותה ולידציה שה־Leader רץ (`ValidateSignedTx`) — באופן עצמאי.
  זו הנקודה — אסור לסמוך על ה־Leader. כל Validator חייב לבדוק לבד.
- אם הכל תקין: חותם על ה־hash של הבלוק, מחזיר אישור.

**מה שחשוב לא לפספס:** ב־`ValidateSignedTx` יש בדיקה של divergence — אם
המחיר שב־tx שונה ב־1% מהמחיר בקאש שלי, אני דוחה. זה נותן הגנה נוספת מפני
מקרה שבו Django מושחת חותם על מחירים מומצאים.

---

## שכבה 6 — שכבת ה־RPC ונקודת הכניסה של ה־Node

**שלושה קבצים שעובדים יחד:**

### `node/internal/server/server.go`
### `node/internal/config/config.go`
### `node/cmd/node/main.go`

`server.go` מממש את שלושת השירותים מה־proto. כל פונקציה היא wrapper דק
מעל המנוע — היא רק מעבירה את הבקשה ל־`Engine` ועוטפת את התשובה בחזרה
לפרוטובאף.

`config.go` קורא קובץ JSON שמכיל את כל ההגדרות של ה־Node: מי הוא
(`node_id`), האם הוא Leader, באיזה פורט הוא מקשיב, איפה המפתח הפרטי שלו,
ומי שלוש הרשויות (כולל הוא עצמו).

`main.go` הוא ה־entrypoint:
1. טוען config.
2. טוען מפתח פרטי.
3. טוען את ה־genesis state.
4. פותח את שרשרת הבלוקים מהדיסק (אם קיימת).
5. בונה את ה־Engine.
6. מרים שרת gRPC.
7. מחכה לסיגנל סיום.

**שלוש הרצות של אותו בינארי, עם שלושה קבצי config שונים = שלושה Nodes.**

---

## שכבה 7 — Django (שכבת המשתמש)

עכשיו עוברים לצד הפייתון. הסדר חשוב פה גם:

### 7א — מודלים (DB schema)

#### `django_app/users/models.py`
#### `django_app/accounts/models.py`
#### `django_app/orders/models.py`

מודל `User` מרחיב את `AbstractUser` של Django ומוסיף שלושה דברים:
`external_id` (המזהה ש־Nodes מכירים), `priv_key_pem`/`pub_key_pem` (מפתחות
ECDSA — נשמרים ב־DB ל־MVP), ו־`nonce` (מונה מקומי, חייב להיות סינכרוני
עם ה־`LastNonce` ב־Node).

מודלי `Wallet` ו־`Position` הם רק **מטמון** של המצב — הם נוחים להצגה ב־UI
אבל מקור האמת הוא ה־Nodes.

מודל `Order` הוא הליבה של מחזור החיים: `DRAFT → SUBMITTED → CONFIRMED/REJECTED`.
כל שדה רלוונטי לכל אחד מהשלבים.

### 7ב — שכבת התקשורת

#### `django_app/orders/grpc_client.py`
#### `django_app/orders/oracle_feed.py`

שני wrappers דקים: אחד ל־gRPC מול ה־Leader, אחד ל־HTTP מול ה־Oracle.

#### `django_app/orders/signer.py`

קראנו את זה כבר בשכבה 1 — חתימת ECDSA בצד פייתון.

### 7ג — Serializers

#### `django_app/orders/serializers.py`

שני serializers: `OrderCreateSerializer` למה שמותר לקבל מהמשתמשת ב־POST,
ו־`OrderSerializer` למה שמוצג חזרה. הוולידציה של הסמל מול ה־whitelist נמצאת
פה.

### 7ד — Views (ההיגיון העסקי בפייתון)

#### `django_app/orders/views.py`

הקובץ הזה הוא ה־API של המערכת. הפונקציה הכי מעניינת היא `submit_order`:

1. נועלת את ה־Order ואת ה־User (`select_for_update`).
2. שואבת ציטוט מחיר טרי מה־Oracle.
3. בודקת limit_price מקומית (לפני שליחה — חוסכת סבב מיותר).
4. בודקת יתרה אופטימית.
5. מקדמת את ה־nonce ב־1.
6. בונה `OrderTx`, חותמת.
7. שולחת `SubmitOrder` ל־Leader.
8. שומרת תוצאה (CONFIRMED או REJECTED).

**מה ללמוד מה־view הזה:** איך עושים thread-safety עם locks ב־DB
(`select_for_update`), ולמה ה־gRPC נמצא **מחוץ** לבלוק ה־transaction —
כדי לא להחזיק נעילה על השורה בזמן שמחכים לרשת.

---

## שכבה 8 — Deployment

### `docker-compose.yml`

מגדיר את כל 5 השירותים: 3 Nodes, Oracle, Django. כולם רצים ברשת אחת
פנימית, ולכן הם יכולים להגיע אחד לשני בשם השירות (`node-1`, `oracle`,
וכו'). ה־ports הפתוחים החוצה הם רק לטובת הדגמה — בפרודקשן רק Django ייחשף.

### `node/Dockerfile`, `django_app/Dockerfile`, `oracle/Dockerfile`

לכל אחד יש Dockerfile שמכין את הסביבה ומריץ `protoc` כדי להפיק את ה־stubs
מהקובץ `proto/trading.proto`.

### `scripts/gen_keys.py` ו־`scripts/wire_configs.py`

שני סקריפטי setup שצריך להריץ פעם אחת לפני ההרצה הראשונה — מייצרים זוגות
מפתחות לכל ה־Nodes ולמשתמשי הדגמה, וממלאים את הקונפיגורציה.

---

## שכבה 9 — תיעוד (התוצרים שהמסמך דורש)

### `ARCHITECTURE.md` — מסמך תכנון וארכיטקטורה
### `THREAT_MODEL.md` — ניתוח איומים ומסקנות
### `README.md` — איך להריץ + smoke test

---

# חלק 4 — סדר לימוד מומלץ (יום אחר יום)

אם יש לך 3-4 ימים לפני ההגנה:

**יום 1 — מושגי בסיס + המוטיב המרכזי:**
- חזרה על blockchain, hashing, digital signatures.
- קוראים את `ARCHITECTURE.md` במלואו.
- קוראים את `proto/trading.proto` ומבינים כל שדה.

**יום 2 — צד ה־Go (הלב של הפרויקט):**
- בוקר: `crypto/ecdsa.go` → `state/state.go` → `blockchain/*`.
- צהריים: `oracleclient/cache.go` → `consensus/poa.go`.
- ערב: `server/server.go` → `cmd/node/main.go`.

**יום 3 — צד הפייתון:**
- `users/models.py` → `accounts/models.py` → `orders/models.py`.
- `orders/signer.py` (ביחד עם `crypto/ecdsa.go` של אתמול).
- `orders/grpc_client.py` → `orders/oracle_feed.py`.
- `orders/views.py` (זה ה־flow השלם — קחי את הזמן).

**יום 4 — Threat model + תרחישי הגנה:**
- קוראים את `THREAT_MODEL.md` במלואו.
- מתרגלים: לכל איום, איפה בקוד היישום של ההגנה?
- מריצים את הפרויקט (`docker compose up`) ומבצעים את ה־smoke test
  שב־`README.md`.

---

# חלק 5 — שאלות שכנראה ישאלו אותך בהגנה (ואיך לענות)

**ש: למה PoA ולא PoW?**
ת: PoW מצריך כוח חישוב יקר וזמני בלוק ארוכים, וזה לא מתאים למסחר. PoA
דורש רק שנכיר מראש את כתובות הרשויות. אצלנו זה הגיוני כי המערכת פנימית.

**ש: מה קורה אם ה־Leader קורס?**
ת: בארכיטקטורה הנוכחית — ה־cluster לא מתקדם (liveness loss, אבל לא
safety loss). זה תועד כ־limitation. הפתרון הוא view-change ו־leader rotation,
שזה הצעד הבא.

**ש: איך אתם מונעים שני Validators מושחתים?**
ת: אנחנו לא — זה הגבול המתמטי של 2-of-3 quorum. הגבול הזה מובנה ב־PoA
ובכל מערכת BFT. הפתרון הוא להגדיל את מספר הרשויות (5-of-7 וכו').

**ש: למה limit_price נבדק בשני מקומות (Django + Node)?**
ת: ב־Django — חוויית משתמש (תגובה מיידית). ב־Node — אבטחה (Django לא
נחשב מקור אמת). אם Django מושחת ולא בודק, ה־Node עדיין יבדוק.

**ש: איך הוולידטור יודע שהפקודה אכן באה מהמשתמשת?**
ת: כל פקודה חתומה ב־ECDSA, והמפתח הציבורי בתוך ה־`SignedOrderTx` נשווה
מול המפתח הרשום בחשבון של אותו משתמש ב־state. גם אם תוקף יזייף תוכן
פקודה — הוא לא יוכל לזייף את החתימה בלי המפתח הפרטי.

**ש: למה הסמלים נמצאים ב־3 מקומות (Django settings, genesis, state)?**
ת: Defense in depth. אם תוקף שולט ב־Django הוא יכול לעקוף את הבדיקה שם,
אבל ה־Nodes עדיין יזרקו את הפקודה כי הסמל לא ב־genesis שלהם.

**ש: מה זה nonce ולמה צריך אותו?**
ת: מספר רץ עולה לכל משתמש. בלי nonce, תוקף שצותת לרשת יכול להפעיל מחדש
את אותה פקודה מספר פעמים. עם nonce, ה־Node זוכר את הערך האחרון שיושם
ודוחה כל בקשה עם nonce שווה או נמוך יותר.

**ש: איפה מתבצע "אכיפת ה־S&P 500"?**
ת: בשלוש שכבות: (1) `OrderCreateSerializer.validate_symbol` ב־Django,
(2) רשימת ה־`symbols` ב־`genesis.json` שכל Node טוען לזיכרון,
(3) `SymbolAllowed` בתוך `state.Validate`.

---

# חלק 6 — מפת הקבצים בקצרה (להדפסה לפני ההגנה)

```
trading-system/
│
├── proto/trading.proto              ← השפה המשותפת (קוראת ראשונה!)
│
├── node/                            ← Go: 3 Nodes הפועלים בקונצנזוס
│   ├── cmd/node/main.go             ← entrypoint
│   ├── internal/
│   │   ├── crypto/ecdsa.go          ← חתימה/אימות ECDSA
│   │   ├── state/state.go           ← state machine + Validate/Apply
│   │   ├── blockchain/
│   │   │   ├── block.go             ← hash, merkle, link validation
│   │   │   └── chain.go             ← append-only storage
│   │   ├── oracleclient/cache.go    ← מטמון מחירים + freshness
│   │   ├── consensus/poa.go         ← ⭐ הליבה: PoA round logic
│   │   ├── server/server.go         ← gRPC handlers
│   │   └── config/config.go         ← טעינת JSON config
│   ├── configs/
│   │   ├── node-1.json (Leader)
│   │   ├── node-2.json (Validator)
│   │   ├── node-3.json (Validator)
│   │   └── genesis.json             ← מצב התחלתי משותף
│   └── Dockerfile
│
├── django_app/                      ← Django: API gateway
│   ├── trading_gateway/             ← settings, urls, wsgi
│   ├── users/models.py              ← User עם מפתחות ECDSA
│   ├── accounts/                    ← Wallet, Position (מטמון)
│   └── orders/
│       ├── models.py                ← Order lifecycle
│       ├── signer.py                ← ⭐ חתימת ECDSA (חייב להתאים ל־Go)
│       ├── grpc_client.py           ← דיבור עם ה־Leader
│       ├── oracle_feed.py           ← דיבור עם ה־Oracle
│       ├── serializers.py           ← validation בכניסה
│       ├── views.py                 ← ⭐ submit_order — ה־flow השלם
│       └── urls.py
│
├── oracle/                          ← Python: שירות מחירים
│   └── oracle.py                    ← yfinance → REST + gRPC push
│
├── scripts/
│   ├── gen_keys.py                  ← הרצה חד־פעמית לפני docker compose
│   └── wire_configs.py
│
├── docker-compose.yml               ← מרים את כל 5 השירותים
│
├── ARCHITECTURE.md                  ← תוצר 1: תכנון
├── THREAT_MODEL.md                  ← תוצר 2: ניתוח איומים
├── README.md                        ← תוצר 3: הוראות הרצה
└── STUDY_GUIDE.md                   ← המסמך הזה
```

⭐ = הקבצים הקריטיים שאסור להגיע להגנה בלי להבין לעומק.

---

# חלק 7 — בדיקה עצמית לפני ההגנה

אחרי שעברת על כל הקבצים, תוכלי לענות על השאלות הבאות *בלי* להציץ בקוד?

1. אם אני שולחת את אותה פקודה פעמיים, מה יקרה בפעם השנייה ולמה?
2. מה קורה אם ה־Oracle מפסיק לעדכן מחירים למשך דקה? פקודות חדשות יתבצעו?
3. אם node-1 מאמין שיש 100,000$ בחשבון של אליס, ו־node-2 מאמין שיש 50,000$,
   מה קרה ואיך נמנע מזה?
4. מה ההבדל בין `Validate` ל־`Apply` ב־`state.go`? למה צריך גם וגם?
5. למה החתימה ב־Python מייצרת בדיוק את אותם בייטים שה־Go מאמת? מה היה
   קורה אם הקידוד היה שונה?
6. בלוק כבר התווסף לשרשרת ב־node-1, אבל node-3 מעולם לא קיבל את ה־CommitBlock.
   מה יקרה כשהוא יבוא?
7. מה ההבדל בין limit order ל־market order במערכת שלנו?
8. תוקף השיג את המפתח הפרטי של אליס. כמה כסף הוא יכול לגנוב? למה לא יותר?

אם את יכולה לענות על השמונה — את מוכנה.

בהצלחה!
