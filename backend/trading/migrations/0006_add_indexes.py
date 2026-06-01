from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ('trading', '0005_order_signature_alter_order_status_userprofile'),
    ]

    operations = [
        # Speed up the leader's SUBMITTED-order poll and the user's order list
        migrations.AddIndex(
            model_name='order',
            index=models.Index(fields=['user', 'status'], name='order_user_status_idx'),
        ),
        # Speed up filtering by status alone (e.g. fetching all SUBMITTED orders)
        migrations.AddIndex(
            model_name='order',
            index=models.Index(fields=['status'], name='order_status_idx'),
        ),
        # Speed up checking how many approvals an order already has
        migrations.AddIndex(
            model_name='orderapproval',
            index=models.Index(fields=['order'], name='approval_order_idx'),
        ),
        # Speed up portfolio view (filter positions by user)
        migrations.AddIndex(
            model_name='position',
            index=models.Index(fields=['user'], name='position_user_idx'),
        ),
    ]
