# Keycloak — דוח מחקר טכני מקיף
> **תפקיד המחבר:** חוקר אבטחת מידע וארכיטקט IAM בכיר
> **תאריך:** אפריל 2026
> **גרסת ייחוס ראשית:** Keycloak 24.x / 25.x

---

## תוכן עניינים

1. [מבוא](#1-מבוא)
2. [פיצ'רים מרכזיים — הסברים מעמיקים](#2-פיצרים-מרכזיים--הסברים-מעמיקים)
3. [מיפוי פיצ'רים ו-APIs](#3-מיפוי-פיצרים-ו-apis)
4. [מחקר אבטחה — CVEs ו-Issues](#4-מחקר-אבטחה--cves-ו-issues)
5. [אימות וטוקנים](#5-אימות-וטוקנים)
6. [גבולות אמון (Trust Boundaries)](#6-גבולות-אמון-trust-boundaries)

---

## 1. מבוא

### 1.1 מה זה Keycloak?

**Keycloak** הוא פתרון קוד-פתוח לניהול זהויות וגישה (**Identity and Access Management — IAM**), המפותח ומתוחזק על-ידי Red Hat תחת קהילת ה-CNCF. הוא מממש סטנדרטים מרכזיים בתעשייה:

- **OpenID Connect (OIDC)** — שכבת זהות מעל OAuth 2.0
- **SAML 2.0** — פרוטוקול פדרציה ו-SSO ארגוני
- **OAuth 2.0** — מסגרת הרשאות
- **LDAP / Kerberos** — אינטגרציה עם ספריות ארגוניות קיימות

מטרתו המרכזית: לאפשר לארגונים לנהל **Single Sign-On (SSO)**, **Multi-Factor Authentication (MFA)**, **Identity Brokering**, וניהול משתמשים — מבלי שהאפליקציות עצמן יצטרכו לממש לוגיקת אימות.

---

### 1.2 ארכיטקטורה כללית

```
┌─────────────────────────────────────────────────────────────┐
│                        Keycloak Server                       │
│                                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │  Realms  │  │  Clients │  │  Users   │  │  Roles & │  │
│  │          │  │ (OIDC/   │  │  Groups  │  │  Scopes  │  │
│  │          │  │  SAML)   │  │          │  │          │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
│                                                             │
│  ┌──────────────────────────────────────────────────────┐  │
│  │              Authentication Flows Engine              │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │  Token   │  │ Identity │  │  User    │  │  Admin   │  │
│  │  Service │  │  Broker  │  │ Storage  │  │   REST   │  │
│  │  (JWT)   │  │  (OIDC/  │  │ (LDAP/  │  │   API    │  │
│  │          │  │  SAML)   │  │  DB)     │  │          │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

### 1.3 מושגי ליבה

| מושג | הגדרה |
|------|--------|
| **Realm** | יחידת הבידוד הבסיסית ב-Keycloak. כל realm מכיל משתמשים, לקוחות, תפקידים ופרוטוקולים משלו. |
| **Client** | אפליקציה רשומה ב-Keycloak שמאומתת בשמה. יכול להיות confidential, public, או bearer-only. |
| **Client Scope** | מגדיר אילו claims ייכנסו ל-token ומה ה-permissions שיועברו לאפליקציה. |
| **User Federation** | מנגנון לחיבור Keycloak למקורות משתמשים חיצוניים (LDAP, Active Directory). |
| **Identity Provider (IDP)** | ספק זהות חיצוני אליו Keycloak מעביר את האימות (Google, Azure AD וכד'). |
| **Identity Brokering** | Keycloak בתפקיד מתווך — מקבל טוקן מ-IDP חיצוני ומנפיק טוקן משלו. |
| **Authentication Flow** | שרשרת של authenticators ו-executions המגדירה תהליך אימות. |
| **SPI (Service Provider Interface)** | מנגנון ה-Plugin של Keycloak להרחבת פונקציונליות. |

---

### 1.4 תרחישי שימוש נפוצים

#### תרחיש 1: SSO ארגוני פנימי
ארגון עם עשרות אפליקציות פנימיות משתמש ב-Keycloak כנקודת אימות מרכזית. העובד מתחבר פעם אחת ומקבל גישה לכל האפליקציות. Keycloak משתלב עם Active Directory דרך LDAP Federation, ומנפיק JWTs לכל אפליקציה.

#### תרחיש 2: Social Login ו-Identity Brokering
פלטפורמת SaaS מאפשרת למשתמשים להתחבר דרך Google, GitHub, או Microsoft. Keycloak משמש כ-Broker: מקבל את ה-OAuth token מהספק החיצוני, ממפה את הקליימים לפרופיל פנימי, ומנפיק access token אחיד לאפליקציה.

#### תרחיש 3: B2B Federation עם SAML
ארגון שמוכר שירותים לחברות אחרות רוצה לאפשר לעובדי הלקוחות לגשת למערכת עם זהויות של ה-IDP הארגוני שלהם (Azure AD, Okta). Keycloak מוגדר כ-SAML SP מול ה-IDPs של הלקוחות, ומנפיק OIDC tokens לאפליקציה.

#### תרחיש 4: Microservices Authorization
מערכת מיקרו-שירותים בה כל שירות מקבל Bearer Token ומאמת אותו מול ה-JWKS endpoint של Keycloak. Fine-Grained Authorization (UMA 2.0) מאפשר לשירותים לשאול את Keycloak האם משתמש מסוים מורשה לפעולה ספציפית.

#### תרחיש 5: Customer-Facing Authentication (CIAM)
פלטפורמת B2C עם מיליוני משתמשים קצה. Keycloak מנהל רישום עצמי, אימות דו-שלבי (TOTP / WebAuthn), שחזור סיסמה, ו-Consent Management לצרכי GDPR.

---

## 2. פיצ'רים מרכזיים — הסברים מעמיקים

---

### 2.1 Single Sign-On (SSO)

SSO הוא אחד הפיצ'רים הבסיסיים והחשובים ביותר של Keycloak. הרעיון הוא פשוט אך עוצמתי: משתמש מתחבר פעם אחת בלבד, ומאותו רגע הוא יכול לגשת לכל האפליקציות של הארגון ללא צורך להתחבר מחדש. זה קורה כי Keycloak שומר Session מרכזי — כאשר האפליקציה הראשונה מבקשת אימות, Keycloak מחזיר token ושומר cookie. כאשר אפליקציה שנייה מבקשת אימות, Keycloak מזהה את ה-session הקיים ומנפיק token חדש מיידית, מבלי להציג מסך כניסה.

מבחינת אבטחה, SSO הוא חרב פיפיות: הוא מפשט את חוויית המשתמש ומצמצם חשיפת סיסמאות, אך יוצר "single point of compromise" — אם session של משתמש נפרץ, התוקף מקבל גישה לכל האפליקציות בבת אחת. לכן SSO חייב להיות מלווה ב-MFA ובמנגנוני Single Sign-Out שמסיימים את ה-session בכל האפליקציות בו-זמנית.

---

### 2.2 Multi-Factor Authentication (MFA)

MFA ב-Keycloak הוא שכבת אבטחה נוספת מעבר לסיסמה. לאחר שהמשתמש הזין סיסמה נכונה, Keycloak דורש ממנו להוכיח את זהותו בדרך נוספת — לרוב קוד חד-פעמי (TOTP) מאפליקציית Authenticator כמו Google Authenticator. הקוד מחושב לפי שעון וסוד משותף, ומתחלף כל 30 שניות — כלומר גם אם תוקף יגנוב את הסיסמה, ללא הקוד הוא לא יוכל להיכנס.

Keycloak תומך במגוון שיטות MFA: TOTP/HOTP קלאסי, WebAuthn/FIDO2 (מפתחות חומרה כמו YubiKey, או ביומטריה כמו Touch ID), וכן Passkeys — סטנדרט מודרני שמחליף לחלוטין את הסיסמה. ניתן להגדיר MFA כ-Conditional: למשל, לדרוש אותו רק ממשתמשים עם תפקיד admin, או רק כאשר ההתחברות מגיעה מ-IP שלא הוכר בעבר.

---

### 2.3 Identity Brokering

Identity Brokering הוא המצב שבו Keycloak פועל כמתווך בין המשתמש לבין ספק זהות חיצוני — כמו Google, GitHub, Azure AD, או Okta. כאשר משתמש לוחץ על "התחבר עם Google", Keycloak מפנה אותו לגוגל, גוגל מאמת אותו ומחזיר token לKeycloak, ואז Keycloak מתרגם את אותו token ל-token פנימי משלו ומחזיר אותו לאפליקציה. מבחינת האפליקציה, לא משנה אם המשתמש אומת דרך גוגל, מיקרוסופט או הזדהות ישירה — היא תמיד מקבלת טוקן אחיד מ-Keycloak.

הדבר החשוב להבין כאן הוא ש-Keycloak לא "סומך עיוור" על ה-IDP החיצוני. הוא מאמת את החתימה הדיגיטלית של הטוקן שהתקבל, בודק שהוא לא פג תוקף, וממפה את הקליימים בזהירות — למשל, כברירת מחדל הוא *לא* מקבל כ-gospel את כתובת המייל שגוגל שולחת, ודורש אימות נפרד, כדי למנוע השתלטות על חשבונות קיימים.

---

### 2.4 User Federation (LDAP / Active Directory)

ארגונים גדולים לרוב כבר מנהלים את המשתמשים שלהם ב-Active Directory או LDAP. User Federation מאפשר ל-Keycloak להתחבר ישירות לספריות אלו ולהשתמש בהן כמקור הזהויות, מבלי לשכפל את כל המשתמשים לתוך מסד הנתונים שלו. כאשר משתמש מנסה להתחבר, Keycloak מעביר את הסיסמה לאימות ל-LDAP, ואם הצליח — יוצר (או מעדכן) פרופיל מקומי.

ניתן לקבוע כיצד Keycloak מסנכרן עם ה-LDAP: סנכרון מלא בזמן הפעלה, סנכרון תקופתי, או "lazy loading" שבו המשתמש נטען לראשונה רק בעת ההתחברות. Federation תומך גם ב-Group Mapping — קבוצות מ-AD ממופות לתפקידים ב-Keycloak, מה שמאפשר לארגון לנהל הרשאות דרך ה-AD שאליו הוא רגיל, ואת Keycloak להשתמש בהן לאכיפה.

---

### 2.5 Authorization Services (Fine-Grained Authorization / UMA 2.0)

הרשאות רגילות (RBAC) עובדות כך: אם יש לך תפקיד "admin" — אתה יכול לעשות הכל. אבל מה אם צריך לומר: "משתמש X יכול לקרוא את הדוקומנט Y, אבל רק בימי עבודה, ורק אם הוא נמצא ברשת הפנימית"? כאן נכנס מנגנון ה-Fine-Grained Authorization של Keycloak, המיישם את סטנדרט UMA 2.0. ניתן להגדיר **Resources** (מה מוגן), **Scopes** (אילו פעולות), **Policies** (כללי ההרשאה) ו-**Permissions** (מיפוי בין הפעולות לכללים).

ה-Policy Engine של Keycloak מאוד גמיש: ניתן לכתוב policies מבוססות תפקיד, מבוססות משתמש ספציפי, מבוססות זמן, מבוססות חוקים ב-JavaScript, ואפילו לשלב מספר policies עם לוגיקה של AND/OR. כאשר שירות רוצה לדעת האם משתמש מורשה לפעולה, הוא שולח שאילתה ל-Keycloak עם ה-resource וה-scope, ו-Keycloak מחזיר החלטה בינארית: מותר / אסור.

---

### 2.6 Realms — בידוד לוגי מלא

Realm הוא המושג המבני החשוב ביותר ב-Keycloak. דמיינו realm כ"שוכר" עצמאי בתוך אותה מערכת: לכל realm יש משתמשים, אפליקציות, תפקידים, מפתחות הצפנה ו-flows משלו, לחלוטין מבודד מ-realms אחרים. ארגון יכול, למשל, ליצור realm נפרד לכל מוצר שלו, לכל סביבה (dev/staging/prod), או לכל לקוח ב-B2B. ה-master realm הוא ה-realm המיוחס — הוא מנהל את כל ה-realms האחרים ומכיל את אדמיני המערכת.

מבחינת אבטחה, הפרדת realms היא קריטית: פריצה ל-realm אחד לא אמורה לתת גישה ל-realm אחר. הסיכון המרכזי הוא misconfiguration — למשל, מתן גישת admin ל-master realm למשתמש שאמור לנהל realm ספציפי בלבד. מפתחות ה-JWT של כל realm שונים, כך שטוקן שהונפק על-ידי realm A לא יהיה תקף ב-realm B.

---

### 2.7 Clients ו-Client Types

Client ב-Keycloak הוא כל אפליקציה שמבקשת לאמת משתמשים דרכו — אתר אינטרנט, אפליקציית מובייל, API, CLI וכד'. ישנם שלושה סוגים עיקריים: **Confidential Client** — שרת-צד שיכול לשמור סוד (client secret) ולאמת את עצמו מול Keycloak בבטחה; **Public Client** — אפליקציית דפדפן או מובייל שלא יכולה לשמור סוד (הקוד חשוף למשתמש), ולכן חייבת להשתמש ב-PKCE; ו-**Bearer-Only Client** — שירות שאינו יוזם login אלא רק מאמת טוקנים שמגיעים אליו (Resource Server).

ההבחנה הזו חשובה מאוד לאבטחה. Public Client שמשתמש ב-client secret הוא בעיית אבטחה קלאסית — כי כל מי שפותח את קוד ה-JavaScript או ה-APK יוכל לראות את הסוד. לכן Public Clients חייבים PKCE: הם יוצרים קוד אקראי לכל בקשה, כך שגם אם מישהו מיירט את ה-authorization code, הוא לא יוכל להחליפו ב-token ללא אותו קוד אקראי.

---

### 2.8 Authentication Flows

Authentication Flow הוא "מתכון" הכניסה של Keycloak — שרשרת של שלבים שמשתמש עובר כדי להתאמת. כל flow בנוי ממספר Executions שיכולים להיות REQUIRED (חובה לעבור), ALTERNATIVE (מספיק לעבור אחד מהם), OPTIONAL (המשתמש יכול לדלג), או DISABLED. הדבר החזק הוא שניתן ליצור flows מותאמים אישית לגמרי: למשל, flow שבו משתמשים "רגילים" מתחברים עם סיסמה בלבד, אך משתמשים עם תפקיד admin נדרשים לסיסמה + WebAuthn.

מבחינה טכנית, כל שלב ב-flow מיושם כ-Authenticator — ממשק Java שניתן לכתוב בעצמך. זה אומר שארגון שצריך אימות מיוחד (למשל: שאילתה ל-API חיצוני לפני אישור, או בדיקת blacklist פנימית) יכול לממש Authenticator מותאם ולחבר אותו ל-flow הקיים. Keycloak מגיע עם עשרות Authenticators מובנים המכסים את רוב צרכי האימות הנפוצים.

---

### 2.9 Token Management ו-JWT

כל האימות ב-Keycloak מתבטא בסופו של דבר ב-Tokens — קבצי JSON חתומים דיגיטלית (JWT) שנשאים מידע על המשתמש ועל הרשאותיו. Access Token הוא הטוקן הקצר-מועד (ברירת מחדל: 5 דקות) שהאפליקציה שולחת לכל בקשת API. ה-Resource Server מאמת את החתימה מול מפתח ציבורי של Keycloak, קורא את ה-roles מתוך הטוקן, ומקבל החלטת גישה — הכל ללא צורך לפנות ל-Keycloak בכל בקשה.

הדינמיקה בין Access Token ל-Refresh Token היא ליבת מנגנון האבטחה: Access Token קצר כי אם נגנב — הנזק מוגבל לדקות ספורות. כאשר פג תוקפו, האפליקציה משתמשת ב-Refresh Token (שמור בצד השרת, ארוך יותר — 30 דקות כברירת מחדל) כדי לקבל Access Token חדש מ-Keycloak, מבלי שהמשתמש יצטרך להתחבר שוב. הגדרה נכונה של חיי טוקנים היא בין ההחלטות הארכיטקטוניות החשובות ביותר: קצר מדי — חוויית משתמש גרועה; ארוך מדי — חלון גדול לתוקפים.

---

### 2.10 Token Mappers ו-Claims

Claims הם ה"מטען" שנמצא בתוך ה-JWT — מידע כמו שם המשתמש, מייל, תפקידים, ועוד. Token Mappers הם ה-mechanism ב-Keycloak שמגדיר אילו claims ייכנסו לאיזה טוקן. ברירת המחדל כוללת claims סטנדרטיים (sub, email, name), אך ניתן להוסיף כל attribute של המשתמש, כולל שדות מותאמים אישית שהוגדרו בפרופיל.

הכוח כאן הוא בגמישות: שירות שצריך לדעת את ה-department של המשתמש או את מספר העובד שלו יכול לקבל את המידע הזה ישירות בטוקן, מבלי לשלוח שאילתה נוספת. הסיכון הוא over-sharing — הכנסת מידע רגיש לטוקן שיכול להיות נקרא על-ידי כל Resource Server שמקבל אותו. כלל הזהב: הכנס ל-Access Token רק את המינימום הנדרש, ומידע רגיש יותר — דרוש למשוך אותו מה-UserInfo Endpoint עם בקשה מאומתת.

---

### 2.11 Admin Console ו-Admin REST API

ה-Admin Console הוא ממשק ה-UI המלא לניהול Keycloak — דרכו ניתן ליצור realms, להגדיר clients, לנהל משתמשים, לשנות authentication flows ועוד. כל פעולה שעושים ב-UI מתרגמת לקריאת Admin REST API מאחורי הקלעים, מה שאומר שכל מה שאפשר לעשות בממשק — אפשר גם לאוטומט. זה קריטי ב-DevOps: ניתן לנהל את כל הגדרות Keycloak כקוד (Infrastructure as Code) דרך Terraform, Ansible, או סקריפטים.

חשוב להבין את מודל ההרשאות של ה-Admin API: כל פעולה דורשת Bearer Token של משתמש שיש לו את ה-role המתאים ב-master realm או ב-realm שמנוהל. ה-API תומך בהרשאות גרנולריות — ניתן לתת למישהו הרשאה לנהל משתמשים בלבד, מבלי לאפשר לו לשנות הגדרות אבטחה. מבחינת תקיפות, ה-Admin API הוא מטרה מרכזית ולכן חייב להיות חשוף רק ל-internal network ולא לאינטרנט הפתוח.

---

### 2.12 Events ו-Audit Logging

Keycloak רושם שני סוגי events: **User Events** — פעולות שמשתמשים מבצעים (login, logout, failed login, register, password change) ו-**Admin Events** — פעולות שמנהלים מבצעים דרך ה-Admin API (יצירת client, שינוי role, עדכון משתמש). כל event כולל timestamp, user ID, IP, realm, וסוג האירוע. ניתן להגדיר שמירת events ב-DB לתקופת retention מוגדרת, ולשלוח events ב-real-time ל-SIEM באמצעות Event Listener SPI.

מבחינת אבטחה וComppliance, ה-Audit Log הוא הרכיב שמאפשר לענות על השאלות החשובות: מי התחבר מתי ומאיפה? מי שינה הרשאות? כמה כשלונות כניסה היו לפני שחשבון ננעל? אילו clients ביקשו tokens? אירועים כמו `LOGIN_ERROR` חוזרים מ-IP ספציפי הם אינדיקטור קלאסי ל-Brute Force Attack. חשוב לוודא שה-events לא מכילים מידע רגיש כמו tokens או סיסמאות בטקסט חשוף — ב-CVE-2023-44483 ראינו בדיוק מה קורה כשלא שמים לב לזה.

---

### 2.13 Themes ו-Customization

Keycloak מציג דפי UI למשתמש בכמה הקשרים: דף הכניסה, דף הרישום, דפי שגיאה, מיילים, וה-Account Console (שבו משתמש יכול לנהל את פרופילו). כל אלה ניתנים להתאמה מלאה דרך מנגנון ה-Themes, המבוסס על שפת תבניות FreeMarker. הארגון יכול לעצב את דף הכניסה לפי המיתוג שלו, להוסיף שדות, לשנות הודעות, ולהתאים לכל שפה.

מעבר ל-UI, Keycloak מציע מערכת SPI (Service Provider Interface) עשירה להרחבת כמעט כל היבט של המערכת בקוד Java: Custom Authenticator (שלב אימות מותאם), Custom Protocol Mapper (claim מותאם), Custom Event Listener (Webhook), ועוד. זה מה שהופך את Keycloak מ"עוד מוצר IAM" למעין IAM Framework — אפשר לבנות עליו כמעט כל לוגיקת אימות והרשאה שניתן לדמיין.

---

### 2.14 High Availability ו-Clustering

בסביבת production רצינית, Keycloak לא רץ כשרת יחיד. הוא מתוכנן לפעול ב-cluster של מספר nodes שמשתפים state דרך **Infinispan** — מסד נתונים in-memory מבוזר. כל ה-sessions, הטוקנים שלא פגו, ונתוני cache משותפים בין כל ה-nodes, כך שאם node אחד נופל — המשתמשים לא מרגישים כלום והם לא נדרשים להתחבר מחדש.

מגרסה 22 ואילך, Keycloak מוכן ל-**Multi-Site Deployment**: פריסה במספר Data Centers עם synchronization בין אתרים, לצורכי Disaster Recovery. הפרסה ב-Kubernetes מנוהלת דרך Operator רשמי, שמטפל באוטומציה בפריסה, scaling, ועדכוני גרסה. נקודת הסיכון המרכזית בסביבות clustered היא שה-Infinispan traffic בין nodes חייב להיות מוצפן ומוגן — אחרת תוקף ברשת הפנימית יכול לקרוא ולזייף session data.

---

## 3. מיפוי פיצ'רים ו-APIs

### 2.1 טבלת מיפוי פיצ'רים מלאה

| קטגוריה | פיצ'ר | תיאור | פרוטוקול רלוונטי |
|---------|--------|--------|-----------------|
| **SSO & Federation** | Single Sign-On | אימות חד-פעמי על פני אפליקציות מרובות | OIDC, SAML 2.0 |
| **SSO & Federation** | Single Sign-Out | התנתקות מרכזית מכל ה-sessions | OIDC Back-Channel Logout, SAML SLO |
| **SSO & Federation** | SAML 2.0 IDP & SP | Keycloak פועל כ-IDP או כ-SP בפדרציה | SAML 2.0 |
| **SSO & Federation** | OIDC Provider | Keycloak כספק OIDC מלא | OIDC / OAuth 2.0 |
| **SSO & Federation** | Kerberos Integration | אימות Kerberos שקוף (SPNEGO) | Kerberos / SPNEGO |
| **Identity Brokering** | Social Login | התחברות דרך Google, GitHub, Facebook וכד' | OAuth 2.0, OIDC |
| **Identity Brokering** | External SAML IDP | פדרציה עם IDPs ארגוניים דרך SAML | SAML 2.0 |
| **Identity Brokering** | External OIDC IDP | פדרציה עם IDPs דרך OIDC | OIDC |
| **Identity Brokering** | Account Linking | קישור חשבון חיצוני לחשבון Keycloak קיים | OIDC |
| **Identity Brokering** | First Login Flow | הגדרת תהליך הרישום בהתחברות ראשונה דרך Broker | Keycloak SPI |
| **User Management** | User Registration | רישום עצמי עם אימות מייל | - |
| **User Management** | User Federation (LDAP/AD) | סנכרון משתמשים מ-LDAP / Active Directory | LDAP v3 |
| **User Management** | Custom User Storage SPI | חיבור למקורות משתמשים מותאמים אישית | Java SPI |
| **User Management** | User Profile Validation | אימות שדות פרופיל בזמן רישום ועדכון | - |
| **User Management** | User Actions (Required Actions) | פעולות נדרשות: שינוי סיסמה, אימות מייל, הגדרת TOTP | - |
| **Authentication** | Username/Password | אימות בסיסי | - |
| **Authentication** | TOTP (Time-Based OTP) | קוד חד-פעמי מבוסס זמן (Google Authenticator) | RFC 6238 |
| **Authentication** | HOTP (HMAC-Based OTP) | קוד חד-פעמי מבוסס מונה | RFC 4226 |
| **Authentication** | WebAuthn / FIDO2 | אימות חסר-סיסמה דרך מפתחות חומרה או ביומטריה | W3C WebAuthn |
| **Authentication** | Passkeys | Passkeys מבוסס WebAuthn | FIDO2 |
| **Authentication** | X.509 Client Certificate | אימות דרך תעודת לקוח | mTLS |
| **Authentication** | Magic Link (Email OTP) | כניסה דרך קישור מייל חד-פעמי | - |
| **Authentication** | Conditional MFA | MFA מותנה לפי IP, role, risk score | - |
| **Authentication** | Brute Force Protection | נעילת חשבון לאחר כשלים חוזרים | - |
| **Authorization** | Role-Based Access Control (RBAC) | תפקידי Realm ו-Client | OAuth 2.0 |
| **Authorization** | Attribute-Based Access Control (ABAC) | הרשאות מבוססות attributes של משתמש | - |
| **Authorization** | Fine-Grained Authorization (UMA 2.0) | ניהול הרשאות ברמת resource עם Policy Engine | UMA 2.0 |
| **Authorization** | Policy Engine | Policies: Role, User, Time, Aggregated, JS, Regex | Keycloak AuthZ |
| **Authorization** | Permission Tickets | UMA 2.0 permission tickets לauthorization אסינכרוני | UMA 2.0 |
| **Token Management** | Access Token | JWT קצר מועד לגישה ל-resources | OAuth 2.0 |
| **Token Management** | Refresh Token | טוקן לחידוש access token | OAuth 2.0 |
| **Token Management** | ID Token | טוקן זהות (OIDC) | OIDC |
| **Token Management** | Token Introspection | בדיקת תקינות טוקן בצד השרת | RFC 7662 |
| **Token Management** | Token Revocation | ביטול טוקן | RFC 7009 |
| **Token Management** | Token Exchange | החלפת טוקן אחד באחר (impersonation, delegation) | RFC 8693 |
| **Token Management** | Pushed Authorization Requests (PAR) | שליחת authorization request ישירות לשרת | RFC 9126 |
| **Token Management** | JWT Token Signing | חתימה RS256, ES256, PS256, HS256 | RFC 7519 |
| **Token Management** | Token Mappers | מיפוי claims מותאם אישית ל-tokens | - |
| **Session Management** | Offline Sessions | sessions ארוכי-טווח (Offline Tokens) | OAuth 2.0 |
| **Session Management** | Session Clustering | Infinispan לשיתוף sessions בין nodes | - |
| **Session Management** | Session Termination | Admin API לסיום sessions ידני | - |
| **Admin & Operations** | Admin Console (UI) | ממשק ניהול גרפי | - |
| **Admin & Operations** | Admin REST API | ניהול מלא דרך API | REST / JSON |
| **Admin & Operations** | Admin CLI (kcadm.sh) | ניהול שורת פקודה | - |
| **Admin & Operations** | Realm Import/Export | ייצוא וייבוא הגדרות realm בפורמט JSON | - |
| **Admin & Operations** | Events & Audit Log | רישום אירועי אבטחה ומנהל | - |
| **Admin & Operations** | Metrics (Prometheus) | חשיפת מטריקות ל-Prometheus | - |
| **Admin & Operations** | Health Check Endpoints | בדיקת בריאות שרת (liveness/readiness) | - |
| **Customization** | Themes (FreeMarker) | התאמת UI ל-login, account, admin, email | - |
| **Customization** | Custom Authenticator SPI | הוספת שלבי אימות מותאמים | Java SPI |
| **Customization** | Custom Event Listener SPI | קבלת אירועים מ-Keycloak (Webhook-style) | Java SPI |
| **Customization** | Custom Protocol Mapper SPI | הוספת claims מותאמים ל-tokens | Java SPI |
| **Customization** | Custom REST Endpoints | הוספת endpoints מותאמים ל-Keycloak | Java SPI |
| **Deployment** | Kubernetes Operator | פריסה אוטומטית ב-Kubernetes | - |
| **Deployment** | High Availability | Clustering עם Infinispan ו-Load Balancer | - |
| **Deployment** | Multi-Site Deployment | פריסה ב-active/passive או active/active | - |
| **Deployment** | Quarkus Runtime | runtime מבוסס Quarkus (מ-v17) | - |
| **Compliance** | GDPR Consent Management | ניהול הסכמת משתמשים | - |
| **Compliance** | PKCE | Proof Key for Code Exchange | RFC 7636 |
| **Compliance** | DPoP | Demonstrating Proof of Possession | RFC 9449 |
| **Compliance** | FAPI 1.0 / FAPI 2.0 | Financial-grade API security profile | FAPI |

---

### 2.2 טבלת מיפוי APIs

#### 2.2.1 OIDC / OAuth 2.0 Endpoints

| Endpoint | Method | תיאור | הרשאות נדרשות |
|----------|--------|--------|----------------|
| `/realms/{realm}/.well-known/openid-configuration` | GET | Discovery document — מחזיר את כל ה-endpoints, signing keys, ו-capabilities של ה-realm | ציבורי |
| `/realms/{realm}/protocol/openid-connect/auth` | GET / POST | Authorization Endpoint — תחילת Authorization Code Flow, Implicit Flow | ציבורי (client_id נדרש) |
| `/realms/{realm}/protocol/openid-connect/token` | POST | Token Endpoint — החלפת code בטוקן, Client Credentials, Resource Owner Password, Refresh Token | client_id + client_secret (confidential) |
| `/realms/{realm}/protocol/openid-connect/userinfo` | GET / POST | UserInfo Endpoint — מחזיר claims על המשתמש המאומת | Bearer Access Token תקף |
| `/realms/{realm}/protocol/openid-connect/logout` | GET / POST | Logout Endpoint — ביצוע logout ו-session termination | Bearer Token / session cookie |
| `/realms/{realm}/protocol/openid-connect/revoke` | POST | Token Revocation Endpoint | client credentials |
| `/realms/{realm}/protocol/openid-connect/introspect` | POST | Token Introspection — בדיקת תקינות וסטטוס טוקן | client credentials (confidential client) |
| `/realms/{realm}/protocol/openid-connect/certs` | GET | JWKS Endpoint — מפתחות ציבוריים לאימות JWT signatures | ציבורי |
| `/realms/{realm}/protocol/openid-connect/auth/device` | POST | Device Authorization Endpoint — Device Flow | client_id |
| `/realms/{realm}/protocol/openid-connect/ext/par/request` | POST | Pushed Authorization Request (PAR) | client credentials |
| `/realms/{realm}/protocol/openid-connect/token` (Token Exchange) | POST | Token Exchange (grant_type=urn:ietf:params:oauth:grant-type:token-exchange) | Bearer Token + scope |

#### 2.2.2 SAML 2.0 Endpoints

| Endpoint | Method | תיאור | הרשאות נדרשות |
|----------|--------|--------|----------------|
| `/realms/{realm}/protocol/saml` | GET / POST | SAML SSO Endpoint — קבלת AuthnRequest מ-SP | ציבורי (חתימה נדרשת) |
| `/realms/{realm}/protocol/saml/descriptor` | GET | SAML IDP Metadata — מתאר XML של ה-IDP | ציבורי |
| `/realms/{realm}/broker/{alias}/endpoint` | POST | Broker Endpoint לקבלת SAML Response מ-IDP חיצוני | ציבורי (חתימה נדרשת) |

#### 2.2.3 Admin REST API

| Endpoint | Method | תיאור | הרשאות נדרשות |
|----------|--------|--------|----------------|
| `/admin/realms` | GET | רשימת כל ה-realms | `manage-realm` (master realm) |
| `/admin/realms` | POST | יצירת realm חדש | `manage-realm` |
| `/admin/realms/{realm}` | GET | קבלת הגדרות realm | `view-realm` |
| `/admin/realms/{realm}` | PUT | עדכון הגדרות realm | `manage-realm` |
| `/admin/realms/{realm}` | DELETE | מחיקת realm | `manage-realm` |
| `/admin/realms/{realm}/users` | GET | רשימת משתמשים (pagination, search) | `view-users` |
| `/admin/realms/{realm}/users` | POST | יצירת משתמש חדש | `manage-users` |
| `/admin/realms/{realm}/users/{userId}` | GET | קבלת פרטי משתמש | `view-users` |
| `/admin/realms/{realm}/users/{userId}` | PUT | עדכון פרטי משתמש | `manage-users` |
| `/admin/realms/{realm}/users/{userId}` | DELETE | מחיקת משתמש | `manage-users` |
| `/admin/realms/{realm}/users/{userId}/reset-password` | PUT | איפוס סיסמה | `manage-users` |
| `/admin/realms/{realm}/users/{userId}/execute-actions-email` | PUT | שליחת מייל עם required actions | `manage-users` |
| `/admin/realms/{realm}/users/{userId}/sessions` | GET | sessions פעילות של משתמש | `view-users` |
| `/admin/realms/{realm}/users/{userId}/logout` | POST | כיבוי כל ה-sessions של משתמש | `manage-users` |
| `/admin/realms/{realm}/users/{userId}/role-mappings` | GET / POST / DELETE | ניהול role mappings של משתמש | `manage-users` |
| `/admin/realms/{realm}/users/{userId}/groups` | GET / PUT / DELETE | ניהול groups של משתמש | `manage-users` |
| `/admin/realms/{realm}/clients` | GET | רשימת clients ב-realm | `view-clients` |
| `/admin/realms/{realm}/clients` | POST | יצירת client חדש | `manage-clients` |
| `/admin/realms/{realm}/clients/{clientId}` | GET / PUT / DELETE | ניהול client | `manage-clients` |
| `/admin/realms/{realm}/clients/{clientId}/secret` | GET / POST | קבלת/חידוש client secret | `manage-clients` |
| `/admin/realms/{realm}/clients/{clientId}/installation/providers/{provider}` | GET | הורדת קובץ הגדרות לאפליקציה | `view-clients` |
| `/admin/realms/{realm}/groups` | GET / POST | ניהול groups | `view-users` / `manage-users` |
| `/admin/realms/{realm}/groups/{groupId}` | GET / PUT / DELETE | ניהול group ספציפי | `manage-users` |
| `/admin/realms/{realm}/roles` | GET / POST | ניהול realm roles | `view-realm` / `manage-realm` |
| `/admin/realms/{realm}/roles/{roleName}` | GET / PUT / DELETE | ניהול role ספציפי | `manage-realm` |
| `/admin/realms/{realm}/identity-provider/instances` | GET / POST | ניהול Identity Providers | `manage-identity-providers` |
| `/admin/realms/{realm}/identity-provider/instances/{alias}` | GET / PUT / DELETE | ניהול IDP ספציפי | `manage-identity-providers` |
| `/admin/realms/{realm}/authentication/flows` | GET / POST | ניהול authentication flows | `manage-realm` |
| `/admin/realms/{realm}/authentication/flows/{flowId}/executions` | GET / PUT | ניהול flow executions | `manage-realm` |
| `/admin/realms/{realm}/events` | GET | קבלת events | `view-events` |
| `/admin/realms/{realm}/events/config` | GET / PUT | הגדרות event logging | `manage-events` |
| `/admin/realms/{realm}/sessions/stats` | GET | סטטיסטיקות sessions | `view-realm` |
| `/admin/realms/{realm}/keys` | GET | מפתחות הצפנה של realm | `view-realm` |
| `/admin/realms/{realm}/client-scopes` | GET / POST | ניהול client scopes | `manage-clients` |
| `/admin/realms/{realm}/components` | GET / POST | ניהול components (User Federation, etc.) | `manage-realm` |

#### 2.2.4 Account Management API (Self-Service)

| Endpoint | Method | תיאור | הרשאות נדרשות |
|----------|--------|--------|----------------|
| `/realms/{realm}/account` | GET / POST | ניהול פרופיל עצמי | Bearer Token (account scope) |
| `/realms/{realm}/account/credentials` | GET | רשימת אישורי כניסה של המשתמש | `account:manage-account` |
| `/realms/{realm}/account/credentials/{credentialId}` | DELETE | מחיקת credential | `account:manage-account` |
| `/realms/{realm}/account/sessions` | GET | sessions פעילות | `account:manage-account` |
| `/realms/{realm}/account/sessions/{sessionId}` | DELETE | ביטול session | `account:manage-account` |

#### 2.2.5 Authorization Services (UMA 2.0)

| Endpoint | Method | תיאור | הרשאות נדרשות |
|----------|--------|--------|----------------|
| `/realms/{realm}/authz/protection/resource_set` | GET / POST | ניהול resources מוגנים | Protection API Token (PAT) |
| `/realms/{realm}/authz/protection/resource_set/{resourceId}` | GET / PUT / DELETE | ניהול resource ספציפי | PAT |
| `/realms/{realm}/authz/protection/permission` | POST | יצירת Permission Ticket | PAT |
| `/realms/{realm}/authz/protection/uma-policy` | GET / POST | ניהול UMA policies | PAT |
| `/realms/{realm}/protocol/openid-connect/token` (RPT) | POST | קבלת Requesting Party Token (RPT) עם הרשאות | Bearer Token + permission ticket |

---

## 3. מחקר אבטחה — CVEs ו-Issues

### 3.1 ניתוח CVEs מרכזיים

הטבלה להלן מציגה את ה-CVEs המשמעותיים ביותר שהתגלו ב-Keycloak לאורך השנים, תוך הדגשת אופן הניצול.

| CVE | שנה | CVSS | קטגוריה | תיאור ואופן ניצול |
|-----|-----|------|---------|-------------------|
| **CVE-2023-6927** | 2023 | 4.6 (Medium) | Open Redirect | פגיעות Open Redirect ב-logout endpoint. תוקף יכול ליצור קישור logout עם `redirect_uri` זדוני שמפנה את הקורבן לאתר פישינג לאחר ביצוע logout. ניצול: שליחת קישור מעוצב לקורבן שנמצא ב-session פעיל. |
| **CVE-2023-44483** | 2023 | 6.5 (Medium) | Information Disclosure | מפתחות פרטיים מהגדרות SAML של realm נכתבו ל-audit log בטקסט חשוף. תוקף עם גישה ל-log files (אך ללא גישת admin ל-Keycloak) יכול לחלץ מפתחות פרטיים ולזייף SAML assertions. |
| **CVE-2023-1664** | 2023 | 6.5 (Medium) | Certificate Validation | בתהליך אימות X.509 client certificate, Keycloak לא ביצע בדיקת Trust Chain תקינה כאשר ה-realm הוגדר ב-"validate certificate" mode. תוקף יכול להציג תעודה עצמית-חתומה (self-signed) ולעבור אימות. |
| **CVE-2022-4361** | 2022 | 8.1 (High) | XSS + Open Redirect | XSS מוחזר (Reflected) ב-login flow דרך פרמטר `redirect_uri` לא מסונן. ניצול: שליחת URL עם payload JavaScript ב-redirect_uri — ה-XSS מתבצע בדפדפן הקורבן בתוך ה-origin של Keycloak, מה שמאפשר גניבת session cookies וביצוע פעולות בשם הקורבן. |
| **CVE-2022-1245** | 2022 | 9.8 (Critical) | Authorization Bypass | פגיעות ב-admin REST API: כאשר Keycloak הוגדר עם Authorization Services, לקוחות עם הרשאות ספציפיות יכלו לקבל גישה ל-admin endpoints ולנהל users ו-roles ללא הרשאה מפורשת. ניצול: שימוש ב-client token ישיר מבלי צורך ב-admin credentials. |
| **CVE-2021-20323** | 2021 | 6.1 (Medium) | XSS | Reflected XSS ב-admin console דרך שדות שלא סוננו כראוי. ניצול מוגבל למי שיש לו גישה ל-admin console. |
| **CVE-2021-3461** | 2021 | 7.1 (High) | Session Fixation | פגיעות Session Fixation ב-client OAuth flows. תוקף שיכול ליזום תהליך כניסה ולמנוע חידוש ה-session ID לאחר האימות, מאפשרת לו לחטוף את ה-authenticated session של קורבן. |
| **CVE-2020-10748** | 2020 | 6.1 (Medium) | CORS Misconfiguration | CORS headers שגויים אפשרו ל-origins לא מורשים לגשת ל-OIDC endpoints. ניצול: Cross-origin request מאתר זדוני שיקרא token או userinfo של משתמש מחובר. |
| **CVE-2020-1718** | 2020 | 8.8 (High) | Authorization Bypass | Improper Privilege Management: תוקף עם גישת admin ל-realm אחד יכול לנצל API כדי לקבל הרשאות ל-master realm. Privilege Escalation בין realms. |
| **CVE-2019-14832** | 2019 | 7.5 (High) | Authorization Bypass | פגיעות ב-UMA (User Managed Access) Policy evaluation. תוקף יכול לנצל חולשה בלוגיקת ה-policy evaluation כדי לקבל גישה ל-resources שאין לו הרשאה עליהם. |
| **CVE-2019-10199** | 2019 | 8.8 (High) | CSRF | CSRF ב-account management console (redirect_uri validation). ניצול: גרימת שינויי חשבון בשם משתמש מחובר דרך בקשה מאתר חיצוני. |
| **CVE-2018-10894** | 2018 | 5.4 (Medium) | SAML Signature Bypass | פגיעות ב-SAML response validation: Keycloak לא בדק כראוי את ה-XML Signature על Assertions בודדות בתוך SAML Response חתום. ניצול: תוקף יכול לשנות את תוכן ה-Assertion (roles, username) מבלי לפסול את החתימה על ה-Response. |
| **CVE-2017-12158** | 2017 | 5.4 (Medium) | XSS | Stored XSS ב-IDP configuration — שם IDP לא סונן כראוי בממשק הניהול. |
| **CVE-2014-3709** | 2014 | 6.8 (Medium) | CSRF | CSRF ב-admin console ב-Keycloak הגרסאות הראשונות — חסרה CSRF token validation. |

---

### 3.2 ניתוח עומק: CVEs קריטיים

#### CVE-2022-1245 — Authorization Bypass (Critical 9.8)

**אופן הניצול המפורט:**

```
תוקף (Client עם הרשאות Authorization Services)
    │
    ▼
POST /admin/realms/{realm}/users
Authorization: Bearer <client_access_token>  ← לא admin token!
    │
    ▼
Keycloak לא מבצע בדיקת הרשאות נכונה
עבור token של client עם policy evaluation permissions
    │
    ▼
יצירת משתמש / שינוי roles ← הצלחה ✓
```

**תנאים לניצול:** הפגיעות מתרחשת כאשר:
1. ה-realm מפעיל Authorization Services
2. לקליינט יש scope כלשהו הקשור ל-Authorization
3. הקליינט מנסה לקרוא ל-Admin API endpoints

---

#### CVE-2018-10894 — SAML XML Signature Bypass

**אופן הניצול:**
SAML Response יכיל מבנה XML עם Response חתום אך Assertion לא חתום:

```xml
<samlp:Response>  ← חתום ✓
  <ds:Signature>...</ds:Signature>
  <saml:Assertion>  ← לא חתום / חתימה מנותקת
    <saml:Subject>
      <saml:NameID>ATTACKER_USER</saml:NameID>  ← מזויף!
    </saml:Subject>
    <saml:AttributeStatement>
      <saml:Attribute Name="Role">
        <saml:AttributeValue>admin</saml:AttributeValue>  ← מזויף!
      </saml:Attribute>
    </saml:AttributeStatement>
  </saml:Assertion>
</samlp:Response>
```

Keycloak קיבל את ה-Response מכיוון שה-Response עצמו היה חתום, אך לא אימת את חתימת ה-Assertion הפנימית.

---

### 3.3 מגמות מ-GitHub Issues

בחינת ה-issues ב-[https://github.com/keycloak/keycloak](https://github.com/keycloak/keycloak) מגלה מגמות מרכזיות:

#### מגמה 1: בעיות Performance תחת עומס גבוה

**Issues חוזרים:**
- Database connection pool exhaustion תחת עומס גבוה
- Memory leaks הקשורים לניהול sessions ב-Infinispan
- איטיות ב-LDAP Federation synchronization עם directories גדולים
- Token introspection endpoints שהופכים ל-bottleneck ב-microservices architectures

**ניתוח:** Keycloak עבר ל-Quarkus runtime (מ-v17), מה שפתר חלק מהבעיות, אך ה-Infinispan cache layer ממשיך להיות מקור לבעיות בסביבות multi-node.

#### מגמה 2: LDAP / Active Directory Integration

**Issues חוזרים:**
- בעיות עם character encoding מיוחדים בשמות LDAP
- Full sync vs Periodic sync failures
- Group hierarchy mapping לא עקבי
- Role mapping failures עם nested AD groups

**ניתוח:** זהו אחד תחומי הכאב הגדולים ביותר, בפרט בסביבות ארגוניות עם AD מורכב. Issues רבים נותרים פתוחים לאורך שנים.

#### מגמה 3: Migration Issues (WildFly → Quarkus)

**Issues חוזרים:**
- Behavioral differences בין ה-runtime החדש לישן
- CLI flags שהשתנו ו-configuration keys שנאמרו
- Extensions ו-SPI implementations שנשברו
- Export/Import formats שאינם backward compatible לחלוטין

**ניתוח:** המעבר ל-Quarkus הכניס רגרסיות משמעותיות. גרסאות 17-21 חוו volume גבוה במיוחד של migration issues.

#### מגמה 4: OIDC Conformance ו-Standards Compliance

**Issues חוזרים:**
- PKCE enforcement לא עקבי
- Back-Channel Logout implementation לא תקני
- DPoP support לא מלא
- FAPI 2.0 compliance gaps

**ניתוח:** Keycloak שואף ל-OIDC Certification אך יש פערים בין ה-spec לבין ה-implementation, בפרט בפיצ'רים חדשים יותר כמו PAR ו-DPoP.

#### מגמה 5: Admin Console UX ו-API Inconsistencies

**Issues חוזרים:**
- Admin API מחזיר response codes לא תקניים
- Pagination לא עקבית ב-admin endpoints
- Race conditions ב-realm import/export
- Admin console חסר validations שגורמים לשגיאות מסתוריות

---

### 3.4 Security Issues פתוחים — מגמות אחרונות

| קטגוריה | תיאור | חומרה משוערת |
|---------|--------|--------------|
| Token Leakage in Logs | access tokens ו-session tokens מופיעים ב-debug logs | Medium |
| Rate Limiting Gaps | אין rate limiting מובנה על token endpoint | Medium-High |
| Admin API Authorization | granularity לא מספיקה ב-admin role permissions | Medium |
| SSRF via IDP Configuration | admin יכול להגדיר IDP URL שמצביע ל-internal resources | Medium |
| Redirect URI Wildcards | אפשרות להגדיר wildcard URIs שמרחיבות את attack surface | Medium |

---

## 4. אימות וטוקנים

### 4.1 מיפוי סוגי Tokens

| סוג Token | פורמט | תוקף טיפוסי | מטרה | Claims עיקריים |
|-----------|--------|-------------|------|----------------|
| **Access Token** | JWT (JWS) | 5 דקות (ברירת מחדל) | גישה ל-resource servers | `sub`, `iss`, `aud`, `exp`, `iat`, `jti`, `scope`, `azp`, `realm_access`, `resource_access` |
| **ID Token** | JWT (JWS) | 5 דקות | זיהוי המשתמש (OIDC בלבד) | `sub`, `iss`, `aud`, `exp`, `iat`, `auth_time`, `nonce`, `name`, `email`, `preferred_username` |
| **Refresh Token** | JWT (JWS) / Opaque | 30 דקות (ברירת מחדל) | חידוש Access Token | `sub`, `jti`, `typ: Refresh` |
| **Offline Token** | JWT (JWS) | ללא פג תוקף (בתנאים) | Refresh Token ארוך-טווח (offline_access scope) | `typ: Offline` + claims רגילים |
| **Client Credentials Token** | JWT (JWS) | 5 דקות | גישה Machine-to-Machine | `sub` = client_id, אין `preferred_username` |
| **Request Object Token (JAR)** | JWT (JWS/JWE) | שימוש חד-פעמי | העברת authorization parameters חתומים | כל OAuth 2.0 request parameters |
| **Logout Token** | JWT (JWS) | שימוש חד-פעמי | Back-Channel Logout notification | `sub`, `sid`, `events`: `"http://schemas.openid.net/event/backchannel-logout"` |
| **Permission Token (RPT)** | JWT (JWS) | 5 דקות | Requesting Party Token עם הרשאות UMA | `authorization.permissions[]` |
| **SAML Assertion** | XML (ds:Signature) | לפי IDP config | SAML SSO | `Subject`, `AttributeStatement`, `Conditions` |
| **Action Token** | JWT (JWS) | בדרך כלל 5-60 דקות | פעולות חד-פעמיות: verify email, reset password | `typ`: action-specific |

---

### 4.2 מבנה Access Token — דוגמת Payload

```json
{
  "exp": 1714300000,
  "iat": 1714299700,
  "auth_time": 1714299700,
  "jti": "a1b2c3d4-...",
  "iss": "https://keycloak.example.com/realms/myrealm",
  "aud": ["account", "my-service"],
  "sub": "user-uuid-...",
  "typ": "Bearer",
  "azp": "my-client",
  "nonce": "xyz...",
  "session_state": "session-uuid-...",
  "acr": "1",
  "allowed-origins": ["https://app.example.com"],
  "realm_access": {
    "roles": ["offline_access", "uma_authorization", "user-role"]
  },
  "resource_access": {
    "my-client": {
      "roles": ["client-specific-role"]
    },
    "account": {
      "roles": ["manage-account", "view-profile"]
    }
  },
  "scope": "openid email profile",
  "sid": "session-uuid-...",
  "email_verified": true,
  "name": "John Doe",
  "preferred_username": "johndoe",
  "given_name": "John",
  "family_name": "Doe",
  "email": "john@example.com"
}
```

---

### 4.3 מיפוי Authentication Flows

#### 4.3.1 Browser Flow (Interactive Login)

```
User → GET /auth → Browser Flow
    │
    ├─ Cookie Check (SSO) → אם יש session תקף → Token הנפקה
    │
    ├─ Username/Password Form
    │   ├─ REQUIRED: Username Password Form
    │   └─ CONDITIONAL: OTP Form (אם המשתמש מוגדר עם OTP)
    │
    ├─ Identity Provider Redirector
    │   └─ Redirect ל-IDP חיצוני אם מוגדר כ-default
    │
    └─ Token Generation → Redirect ל-client redirect_uri עם ?code=...
```

#### 4.3.2 טבלת כל ה-Authentication Flows

| Flow | תיאור | סוג Grant / שימוש |
|------|--------|-------------------|
| **Browser Flow** | זרימת אימות מלאה בדפדפן עם UI | Authorization Code Flow |
| **Direct Grant Flow** | אימות ישיר עם username/password (ללא redirect) | Resource Owner Password Credentials Grant |
| **Registration Flow** | תהליך הרשמה עצמית של משתמש חדש | - |
| **Reset Credentials Flow** | שחזור סיסמה / credentials | - |
| **Client Authentication Flow** | אימות הקליינט עצמו (client secret / JWT) | Client Credentials Grant |
| **First Broker Login Flow** | אימות/הרשמה בפעם הראשונה דרך IDP חיצוני | Identity Brokering |
| **Post Broker Login Flow** | פעולות נוספות לאחר broker login (MFA override) | Identity Brokering |
| **HTTP Challenge Flow** | Non-browser clients (curl, CLI tools) | Basic Auth / Digest |
| **Docker v2 Flow** | אימות Docker registry clients | Docker v2 Token Auth |
| **Service Account Flow** | Machine-to-Machine authentication | Client Credentials Grant |

#### 4.3.3 Authorization Code Flow (הפופולרי ביותר)

```
┌─────────┐     1. GET /auth?               ┌───────────┐
│ Browser │ ──────────────────────────────> │ Keycloak  │
│         │     response_type=code          │           │
│         │     client_id, redirect_uri     │           │
│         │     scope, state, nonce         │           │
│         │                                 │           │
│         │ <────────────────────────────── │           │
│         │  2. Login Page                  │           │
│         │                                 │           │
│         │  3. POST credentials ─────────> │           │
│         │                                 │           │
│         │ <────────────────────────────── │           │
│         │  4. Redirect: ?code=AUTH_CODE   │           │
│         │     &state=STATE                │           │
└────┬────┘                                 └───────────┘
     │                                            ▲
     │  5. GET redirect_uri?code=AUTH_CODE        │
     ▼                                            │
┌─────────┐                                       │
│   App   │  6. POST /token                       │
│ Server  │     code=AUTH_CODE                    │
│         │     client_id + client_secret ────────┘
│         │
│         │  7. Receives: access_token, id_token, refresh_token
└─────────┘
```

#### 4.3.4 Authentication Executions (Building Blocks)

| Authenticator | תיאור | שימוש ב-Flow |
|---------------|--------|-------------|
| `auth-cookie` | בדיקת SSO cookie קיים | Browser Flow |
| `auth-spnego` | SPNEGO/Kerberos authentication | Browser Flow |
| `identity-provider-redirector` | הפניה ל-IDP חיצוני | Browser Flow |
| `auth-username-password-form` | טופס username/password | Browser Flow, Direct Grant |
| `auth-otp-form` | טופס TOTP/HOTP | Browser Flow (Conditional) |
| `webauthn-authenticator` | WebAuthn/FIDO2 authentication | Browser Flow |
| `webauthn-authenticator-passwordless` | WebAuthn ללא סיסמה | Browser Flow |
| `direct-grant-validate-username` | אימות username ב-Direct Grant | Direct Grant Flow |
| `direct-grant-validate-password` | אימות password ב-Direct Grant | Direct Grant Flow |
| `direct-grant-validate-otp` | אימות OTP ב-Direct Grant | Direct Grant Flow |
| `reset-credentials-choose-user` | בחירת משתמש לאיפוס | Reset Credentials Flow |
| `reset-otp` | הגדרת OTP חדש | Reset Credentials Flow |
| `reset-password` | הגדרת סיסמה חדשה | Reset Credentials Flow |
| `idp-create-user-if-unique` | יצירת משתמש אם לא קיים | First Broker Login Flow |
| `idp-confirm-link` | אישור קישור חשבון קיים | First Broker Login Flow |
| `idp-email-verification` | אימות מייל בעת broker login | First Broker Login Flow |
| `idp-username-password-form` | username/password לאישור קישור | First Broker Login Flow |
| `client-secret` | אימות client secret | Client Authentication Flow |
| `client-secret-jwt` | אימות JWT חתום ב-client secret | Client Authentication Flow |
| `client-jwt` | אימות JWT חתום ב-private key | Client Authentication Flow |
| `client-x509` | אימות X.509 certificate | Client Authentication Flow |
| `conditional-user-configured` | Conditional מבוסס הגדרת משתמש | כל flow עם MFA |
| `conditional-level-of-authentication` | Conditional מבוסס ACR (LoA) | Browser Flow |

#### 4.3.5 Grant Types הנתמכים

| Grant Type | שימוש | OAuth RFC |
|-----------|--------|-----------|
| `authorization_code` | Browser-based apps | RFC 6749 |
| `implicit` (Deprecated) | Legacy SPAs (לא מומלץ) | RFC 6749 |
| `password` (ROPC) | Legacy/trusted apps בלבד | RFC 6749 |
| `client_credentials` | Machine-to-Machine | RFC 6749 |
| `refresh_token` | חידוש tokens | RFC 6749 |
| `urn:ietf:params:oauth:grant-type:device_code` | Device Flow (TV, CLI) | RFC 8628 |
| `urn:ietf:params:oauth:grant-type:token-exchange` | Token Exchange | RFC 8693 |
| `urn:openid:params:grant-type:ciba` | CIBA (Backchannel Authentication) | OIDC CIBA |

---

## 5. גבולות אמון (Trust Boundaries)

### 5.1 מודל גבולות האמון הכללי

```
╔═══════════════════════════════════════════════════════════════╗
║                    EXTERNAL (Untrusted Zone)                  ║
║   ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐   ║
║   │   End Users │  │  Browsers   │  │  External IDPs      │   ║
║   │  (Internet) │  │             │  │  (Google, GitHub,   │   ║
║   │             │  │             │  │   Azure AD, Okta)   │   ║
║   └──────┬──────┘  └──────┬──────┘  └──────────┬──────────┘   ║
╚══════════╪════════════════╪════════════════════|══════════════╝
           │  TLS/HTTPS     │  TLS/HTTPS         │ TLS/HTTPS
═══════════╪════════════════╪════════════════════╪═════════════
           ▼                ▼                    ▼
╔══════════════════════════════════════════════════════════════╗
║                    DMZ / Keycloak Zone                       ║
║   ┌────────────────────────────────────────────────────────┐ ║
║   │                   KEYCLOAK SERVER                      | ║
║   │                                                        │ ║
║   │  ┌─────────────┐        ┌───────────────────────────┐  │ ║
║   │  │  Public     │        │  Admin Zone (Protected)   │  │ ║
║   │  │  Endpoints  │        │  /admin/*                 │  │ ║
║   │  │  /realms/*  │        │  Admin Console            │  │ ║
║   │  └─────────────┘        └───────────────────────────┘  │ ║
║   └────────────────────────────────────────────────────────┘ ║
╚══════════╪═══════════════════════════════════════════════════╝
           │  Internal Network
═══════════╪══════════════════════════════════════════════════
           ▼
╔═════════════════════════════════════════════════════════════╗
║                   INTERNAL (Trusted Zone)                   ║
║   ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ ║
║   │  Resource   │  │  Database   │  │  LDAP/AD            │ ║
║   │  Servers    │  │  (Postgres) │  │  Directory          │ ║
║   │  (APIs)     │  │             │  │                     │ ║
║   └─────────────┘  └─────────────┘  └─────────────────────┘ ║
╚═════════════════════════════════════════════════════════════╝
```

---

### 5.2 מטריצת גבולות האמון

| ישות מקור | ישות יעד | מה מועבר | רמת אמון | מנגנון אמות |
|-----------|---------|---------|-----------|-------------|
| End User / Browser | Keycloak (Public Endpoints) | Credentials, Tokens, Session Cookies | **לא מהימן** | אימות credentials, PKCE, CSRF tokens |
| End User / Browser | Keycloak (Admin Console) | Admin credentials | **לא מהימן** | אימות חזק, MFA מומלץ |
| Keycloak | Resource Server (API) | JWKS (מפתחות ציבוריים) | **מהימן** | TLS + Token signing (RS256/ES256) |
| Resource Server | Keycloak (Introspection) | Access Token לאימות | **מהימן חלקית** | TLS + client credentials |
| External IDP | Keycloak (Broker) | OIDC Token / SAML Assertion | **מהימן מוגבל** | Signature validation + מיפוי claims |
| Keycloak | External IDP | Authorization Request | **מהימן מוגבל** | TLS + client secret |
| Admin CLI / CI | Keycloak (Admin API) | Management requests | **לא מהימן** | Bearer Token (admin scope) + TLS |
| Keycloak | LDAP/AD | LDAP queries + User data | **מהימן פנימי** | StartTLS/LDAPS + bind credentials |
| Keycloak | Database | Session data, User storage | **מהימן מלא** | Network ACL + DB credentials |
| Keycloak Node A | Keycloak Node B (Cluster) | Infinispan cache data | **מהימן מלא** | Network isolation + JGroups encryption |

---

### 5.3 ניתוח מעמיק: Keycloak כ-Identity Broker מול IDP חיצוני

זהו אחד התרחישים המורכבים ביותר מבחינת גבולות אמון, ומצריך ניתוח קפדני.

#### 5.3.1 מבנה הסיטואציה

```
                    ┌──────────────────────┐
                    │   External IDP       │
                    │   (e.g., Azure AD,   │
                    │    Okta, Google)     │
                    └──────────┬───────────┘
                               │
                    TRUSTS ◄───┤ (IDP issues tokens
                               │  that Keycloak accepts)
                               │
                    ┌──────────▼───────────┐
                    │   KEYCLOAK (Broker)  │
                    │                      │
                    │  - Validates IDP     │
                    │    tokens/assertions │
                    │  - Maps claims       │
                    │  - Issues own tokens │
                    └──────────┬───────────┘
                               │
                    TRUSTS ◄───┤ (Keycloak issues tokens
                               │  that Apps accept)
                               │
              ┌────────────────▼─────────────────┐
              │           Applications           │
              │  (Resource Servers, SPAs, etc.)  │
              └──────────────────────────────────┘
```

#### 5.3.2 זרימת האמון המפורטת

**שלב 1: הגדרת האמון ב-Keycloak**

כאשר מגדירים IDP חיצוני ב-Keycloak:

```json
// IDP Configuration in Keycloak
{
  "alias": "azure-ad",
  "providerId": "oidc",
  "enabled": true,
  "trustEmail": false,           // ← קריטי! האם לסמוך על מייל מהIDP?
  "storeToken": false,           // ← האם לשמור את ה-token של ה-IDP?
  "addReadTokenRoleOnCreate": false,
  "config": {
    "issuer": "https://login.microsoftonline.com/{tenant}/v2.0",
    "jwksUrl": "https://login.microsoftonline.com/{tenant}/discovery/v2.0/keys",
    "clientId": "keycloak-app-id",
    "clientSecret": "...",
    "validateSignature": "true",  // ← חובה!
    "useJwksUrl": "true"
  }
}
```

**שלב 2: אימות ה-Token מה-IDP**

Keycloak מבצע את השלבים הבאים לאימות ה-token שהתקבל:

```
External IDP Token Validation Process:
1. בדיקת Signature (RS256/ES256) מול JWKS של ה-IDP
2. בדיקת iss claim מול ה-issuer המוגדר
3. בדיקת aud claim (שיכיל את ה-client_id של Keycloak)
4. בדיקת exp claim (תפוגת token)
5. בדיקת nonce (אם הושלח) — מניעת Replay Attacks
6. בדיקת azp / authorized_party (אם רלוונטי)
```

**שלב 3: Claim Mapping**

לאחר אימות הטוקן, Keycloak ממפה את הקליימים מהIDP הפנימי שלו:

```
External IDP Token Claims          Keycloak User Attributes
─────────────────────────────────────────────────────────
sub: "external-user-id"     →     federatedIdentity.userId
email: "user@company.com"   →     user.email (אם trustEmail=true)
name: "John Doe"            →     user.displayName
groups: ["admins"]          →     (תלוי במיפוי) → roles
custom_claim: "value"       →     user.attribute.custom_claim
```

ניתוח גבולות האמון — מי סומך על מי?
**שכבה 1: IDP חיצוני סומך על Keycloak (כ-Registered Client)**
```
Azure AD / External IDP
    │
    ├─ TRUSTS Keycloak כ-OAuth2 Client
    │   ├─ Keycloak רשום כ-App ב-Azure AD
    │   ├─ client_id + client_secret (או PKCE)
    │   └─ redirect_uri מאושר: https://keycloak.example.com/realms/{realm}/broker/{alias}/endpoint
    │
    └─ אמון מבוסס על: TLS, Client Registration, Redirect URI Validation
```

**המשמעות הביטחונית:** אם תוקף מצליח לשנות את ה-redirect_uri המוגדר ב-IDP החיצוני, הוא יכול לגנוב את ה-authorization code ולהתחזות לכל משתמש.

---

**שכבה 2: Keycloak סומך על IDP חיצוני (Accepting Claims)**
```
Keycloak (Broker)
    │
    ├─ TRUSTS External IDP לנפק זהויות תקפות
    │   ├─ אמות JWT Signature מול JWKS של ה-IDP
    │   ├─ בדיקת Issuer (iss)
    │   └─ מיפוי claims פנימי
    │
    ├─ PARTIAL TRUST — Keycloak לא סומך עיוור
    │   ├─ אינו מקבל roles/groups ישירות (ברירת מחדל)
    │   ├─ מפעיל First Broker Login Flow לאישור
    │   └─ מגדיר trustEmail=false (ברירת מחדל)
    │
    └─ TRUST BOUNDARY: Keycloak הוא "גשר" — הוא מאמת את הזהות
       אך לא בהכרח את ההרשאות מה-IDP החיצוני
```

---

**שכבה 3: אפליקציות סומכות על Keycloak (Accepting Keycloak Tokens)**
```
Applications (Resource Servers)
    │
    ├─ FULLY TRUST Keycloak Tokens
    │   ├─ אמות JWT Signature מול Keycloak JWKS
    │   ├─ בדיקת iss: https://keycloak.example.com/realms/{realm}
    │   └─ קריאת roles מ-realm_access / resource_access
    │
    └─ האפליקציה אינה יודעת מה מקור הזהות (local / broker)
       — זו נקודת הכוח וגם נקודת הסיכון!
```

וקטורי תקיפה ייחודיים לתרחיש Broker
**וקטור 1: IDP Claim Injection / Claim Confusion**

```
תרחיש:
IDP חיצוני (שנפרץ / לא מהימן מספיק) מנפיק token עם:
{
  "sub": "admin",
  "email": "real-admin@company.com",
  "groups": ["Administrators"]
}

סיכון:
אם Keycloak מוגדר עם mapper שמעתיק groups → roles,
תוקף יוכל להפוך לאדמין ב-Keycloak realm.

הגנה:
- אל תמפה groups/roles ישירות מ-IDP חיצוני לפי שם בלבד
- השתמש במיפוי מפורש ומוגבל
- הפעל First Broker Login Flow עם manual approval
- trustEmail=false (ברירת מחדל)
```

**וקטור 2: Account Takeover דרך Email Trust**

```
תרחיש:
- משתמש A קיים ב-Keycloak עם email: victim@company.com
- IDP חיצוני (גוגל) מנפיק token עם email: victim@company.com
- trustEmail=true מוגדר ב-IDP config

תוצאה:
Keycloak מחבר את ה-broker login לחשבון הקיים של victim,
ומאפשר לכל מי שיש לו גוגל account עם אותו מייל להשתלט על החשבון!

הגנה:
- trustEmail=false (ברירת מחדל ומומלץ)
- דרוש Email Verification ב-First Broker Login Flow
- הפעל Existing User by Email Verification
```

**וקטור 3: Sub Claim Collision**

```
תרחיש:
IDP חיצוני A מנפיק sub: "12345"
IDP חיצוני B (IDP אחר) מנפיק sub: "12345"

כאשר שני IDPs מחוברים ל-Keycloak,
אם הממשות אינה מבדילה לפי alias + sub,
עלול להיווצר קישור שגוי בין חשבונות.

הגנה:
Keycloak מאחסן פדרציית זהויות כ-{alias}:{sub}
— הפרדה בין IDPs מובנית, אך חשוב לאמת
```

**וקטור 4: Token Substitution ב-Broker Flow**

```
תרחיש (Man-in-the-Middle):
1. קורבן מתחיל Broker Login Flow
2. תוקף מיירט את authorization code (אם TLS נפגע / code leakage)
3. תוקף מחליף את ה-code בשלב החלפת ה-token

הגנה:
- PKCE (חובה עם code_verifier) — RFC 7636
- state parameter validation
- Strict TLS Pinning
- nonce validation ב-ID Token
```

#### 5.3.5 Claim Mapping — ניתוח אבטחה

כאשר Keycloak פועל כ-Broker, הוא מבצע מיפוי claims מהIDP החיצוני:

```
┌─────────────────────────────────────────────────────────────┐
│              Claim Mapping Trust Analysis                   │
│                                                             │
│  External IDP Claim    │  Risk Level  │  Recommendation     │
│  ────────────────────────────────────────────────────────   │
│  sub                   │  LOW         │  תמיד מאוחסן        │
│                        │              │  כ-federatedId     │
│  email                 │  HIGH        │  trustEmail=false   │
│                        │              │  + verification     │
│  roles / groups        │  CRITICAL    │  אל תמפה ישירות!    │
│                        │              │  מיפוי מפורש בלבד │
│  name / username       │  MEDIUM      │  אפשר עם review     │
│  custom attributes     │  MEDIUM-HIGH │  whitelist בלבד     │
│  acr / amr             │  MEDIUM      │  אמות מול מדיניות   │
└─────────────────────────────────────────────────────────────┘
```

מצב שבו ה-IDP החיצוני סומך על Keycloak
זהו תרחיש הפוך מהרגיל — כאשר Keycloak מוגדר כ-**IDP** (לא כ-SP) ומערכת חיצונה סומכת עליו.

```
          ┌─────────────────────────────────────────┐
          │  External System (e.g., AWS Cognito,    │
          │  another Keycloak, Enterprise App)       │
          │                                         │
          │  מוגדר כ-SP / Relying Party             │
          │  מחכה ל-assertions מ-Keycloak           │
          └────────────────┬────────────────────────┘
                           │ TRUSTS Keycloak
                           │ (אימות Keycloak Tokens/Assertions)
                           │
          ┌────────────────▼────────────────────────┐
          │           KEYCLOAK (as IDP)             │
          │                                         │
          │  מנפיק:                                 │
          │  - SAML Assertions (ל-SAML SPs)         │
          │  - OIDC Tokens (ל-OIDC RPs)             │
          │  - JWT Access Tokens                    │
          └─────────────────────────────────────────┘
```

**כאשר מערכת חיצונית סומכת על Keycloak כ-IDP, היא מבצעת:**

| פעולת אמות | מה נבדק | סיכון אם נכשל |
|-----------|--------|--------------|
| Signature Validation | JWT חתום ב-RS256/ES256 עם מפתח Keycloak | Token Forgery |
| Issuer Validation | `iss` = `https://keycloak.../realms/{realm}` | Cross-realm Attack |
| Audience Validation | `aud` מכיל את שם האפליקציה | Token Misuse |
| Expiry Validation | `exp` לא עבר | Replay Attack |
| SAML Signature | XML ds:Signature על ה-Assertion | Assertion Forgery |
| SAML Conditions | NotBefore / NotOnOrAfter | Replay Attack |
| SAML Recipient | ה-ACS URL שמוגדר ב-SP | Cross-site Assertion |

**נקודות בטחון קריטיות כאשר IDP חיצוני סומך על Keycloak:**

1. **Key Rotation:** Keycloak מחייב rotation של signing keys. הIDP החיצוני חייב להוריד JWKS מחדש בעת rotation.
2. **Realm Isolation:** כל realm ב-Keycloak עצמאי. IDP חיצוני שסומך על realm X לא אמור לסמוך על realm Y (שגיאה נפוצה בהגדרה).
3. **Client Validation:** Keycloak חייב לוודא ש-SP רשום כ-client לפני שהוא מנפיק token/assertion.
4. **Token Exchange Risk:** אם Token Exchange מופעל, IDP חיצוני שמקבל Keycloak token צריך לדעת שה-subject יכול להיות user חיצוני (מ-broker).

דיאגרמת Trust Chain מלאה
```
External IDP (Azure AD)
        │
        │ 1. Issues OIDC Token for User X
        │    (sig: RS256, iss: login.microsoftonline.com)
        │
        ▼
KEYCLOAK (Broker)
        │
        │ 2. Validates Azure AD Token
        │    - Checks sig vs Azure JWKS
        │    - Maps: sub + email → Keycloak User
        │    - Runs: First Broker Login Flow
        │    - IGNORES external roles (by design)
        │
        │ 3. Issues its own Access Token
        │    (sig: RS256, iss: keycloak.example.com/realms/myrealm)
        │    Claims include ONLY Keycloak-defined roles
        │
        ▼
Application (Resource Server)
        │
        │ 4. Validates Keycloak Token
        │    - Checks sig vs Keycloak JWKS
        │    - Checks iss = keycloak.example.com/realms/myrealm
        │    - Reads realm_access.roles
        │    - Makes authorization decision
        │
        ▼
DECISION: Allow / Deny (based on Keycloak-managed roles only)

KEY INSIGHT:
============
האפליקציה אינה יודעת שהמשתמש הגיע מ-Azure AD.
Keycloak הוא "תרגומי" — מבדד את האפליקציה מ-IDP החיצוני.
זה גם הכוח (abstraction) וגם הסיכון (single point of failure).
```

---

המלצות לחיזוק גבולות האמון
| תחום | המלצה | חומרה |
|------|--------|-------|
| **Broker Configuration** | הגדר `trustEmail=false` תמיד; אמת מייל בנפרד | קריטי |
| **Broker Configuration** | אל תמפה roles/groups מ-IDP חיצוני לפי שם — השתמש בממוני מפורשים | קריטי |
| **Broker Configuration** | הפעל First Broker Login Flow עם Email Verification | גבוה |
| **Token Security** | הפעל PKCE (`S256`) לכל public clients | קריטי |
| **Token Security** | הגבל את חיי ה-Access Token ל-5 דקות | גבוה |
| **Token Security** | השתמש ב-Refresh Token Rotation | גבוה |
| **Admin API** | חסום גישה ל-`/admin/*` ל-internal network בלבד | קריטי |
| **Admin API** | הפעל MFA על admin accounts | קריטי |
| **Key Management** | הגדר Key Rotation Policy (למשל כל 90 יום) | גבוה |
| **Network** | הפעל `X-Frame-Options: DENY` ו-`Content-Security-Policy` | גבוה |
| **Logging** | הפעל Event Logging לכל login events + failures | גבוה |
| **Brute Force** | הגדר Brute Force Protection עם lockout policy | גבוה |
| **SAML** | אמות תמיד את חתימת ה-Assertion, לא רק ה-Response | קריטי |
| **Cluster** | הצפן את Infinispan traffic בין nodes | גבוה |
| **IDP Trust** | עדכן JWKS של IDP חיצוני אוטומטית — אל תסמוך על cached keys בלבד | גבוה |

---

## נספח: Keycloak מול מתחרים

| קריטריון | Keycloak | Auth0 | Okta | Azure AD |
|---------|----------|-------|------|----------|
| **סוג** | Open Source | SaaS | SaaS | SaaS/Hybrid |
| **עלות** | חינם (self-hosted) | תשלום לפי MAU | תשלום לפי משתמש | תשלום לפי תכונה |
| **OIDC** | ✅ מלא | ✅ מלא | ✅ מלא | ✅ מלא |
| **SAML 2.0** | ✅ | ✅ | ✅ | ✅ |
| **WebAuthn** | ✅ | ✅ | ✅ | ✅ |
| **Fine-Grained Authz** | ✅ (UMA 2.0) | ⚠️ חלקי | ⚠️ חלקי | ⚠️ |
| **Custom Flows** | ✅ מלא (SPI) | ✅ (Actions) | ✅ (Inline Hooks) | ⚠️ מוגבל |
| **Self-hosted** | ✅ | ❌ | ❌ | ⚠️ ADFS בלבד |
| **Kubernetes** | ✅ Operator | ❌ | ❌ | ❌ |
| **Enterprise Support** | ✅ (Red Hat) | ✅ | ✅ | ✅ (Microsoft) |

---

*דוח זה הוכן על בסיס תיעוד רשמי של Keycloak (https://www.keycloak.org/docs), מאגרי CVE (NVD/MITRE), ומחקרי אבטחה ציבוריים. יש לעדכנו בהתאם לגרסאות חדשות.*
