import yfinance as yf
from datetime import datetime, timezone
import json

def get_stock_data(ticker_symbol):
    """
    פונקציה שפונה ל-Yahoo Finance, שולפת את המחיר הנוכחי של המניה
    ומחזירה אותו יחד עם חותמת זמן מדויקת (UTC).
    """
    try:
        # יצירת אובייקט מניה
        stock = yf.Ticker(ticker_symbol)
        
        # שליפת היסטוריית המחירים של היום האחרון
        todays_data = stock.history(period='1d')
        
        if todays_data.empty:
            return {"error": f"לא נמצאו נתונים עבור המניה {ticker_symbol}"}

        # לקיחת מחיר הסגירה/הנוכחי האחרון
        current_price = round(todays_data['Close'].iloc[-1], 2)
        
        # יצירת חותמת זמן בפורמט ISO (אותו פורמט ש-Django מצפה לקבל)
        current_timestamp = datetime.now(timezone.utc).isoformat()

        # החזרת הנתונים כ-Dictionary (מוכן להמרה ל-JSON)
        return {
            "ticker": ticker_symbol,
            "execution_price": str(current_price),
            "timestamp": current_timestamp
        }

    except Exception as e:
        return {"error": str(e)}

# --- קוד לבדיקה מקומית ---
if __name__ == "__main__":
    print("מתחבר לאורקל ושולף נתונים עבור Apple (AAPL)...")
    result = get_stock_data("AAPL")
    
    # נדפיס את התוצאה בצורה יפה כדי לראות מה חוזר
    print(json.dumps(result, indent=4, ensure_ascii=False))