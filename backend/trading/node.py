import requests
import time
from oracle import get_stock_data # מייבאים את האורקל שבנינו קודם!

# הגדרות חיבור ל-Backend
BASE_URL = "http://127.0.0.1:8000/api"
# ה-Node צריך משתמש מנהל (Superuser) כדי למשוך את כל ההזמנות
NODE_USER = "nw" 
NODE_PASS = "1234"

def run_node():
    print("🚀 Execution Node הופעל ומתחיל לסרוק הזמנות...")
    
    # לולאה אינסופית - ה-Node תמיד מאזין לעבודה חדשה
    while True:
        try:
            # 1. בקשת כל ההזמנות מה-API (שימוש ב-Basic Auth לשם הפשטות)
            response = requests.get(f"{BASE_URL}/orders/", auth=(NODE_USER, NODE_PASS))
            
            if response.status_code == 200:
                orders = response.json()
                # מסננים רק את ההזמנות שממתינות לאישור
                submitted_orders = [o for o in orders if o['status'] == 'SUBMITTED']
                
                for order in submitted_orders:
                    print(f"⏳ נמצאה הזמנה ממתינה! מזהה: {order['id']}, מניה: {order['stock']}")
                    
                    # 2. פנייה ל-Oracle לקבלת מחיר עדכני וחותמת זמן
                    oracle_data = get_stock_data(order['stock'])
                    
                    if "error" in oracle_data:
                        print(f"❌ שגיאה בקבלת נתונים מהאורקל: {oracle_data['error']}")
                        continue
                        
                    # 3. שליחת אישור הביצוע ל-Django (כולל מחיר ו-timestamp)
                    payload = {
                        "execution_price": oracle_data['execution_price'],
                        "timestamp": oracle_data['timestamp']
                    }
                    
                    print(f"⬅️ שולח אישור ביצוע ל-Django לפי מחיר {oracle_data['execution_price']}...")
                    
                    exec_response = requests.post(
                        f"{BASE_URL}/orders/{order['id']}/execute_order/",
                        json=payload,
                        auth=(NODE_USER, NODE_PASS)
                    )
                    
                    if exec_response.status_code == 200:
                        print(f"✅ הזמנה {order['id']} אושרה ובוצעה בהצלחה!\n")
                    else:
                        print(f"⚠️ שגיאה מול ה-Backend: {exec_response.text}\n")
                        
        except requests.exceptions.ConnectionError:
            print("❌ לא מצליח להתחבר ל-Django. האם השרת (runserver) דולק?")
            
        # המתנה של 5 שניות לפני הסריקה הבאה כדי לא להציף את השרת
        time.sleep(5)

if __name__ == "__main__":
    run_node()